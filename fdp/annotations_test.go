// Copyright 2020-2026 Buf Technologies, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package fdp_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/trendvidia/protocompile/fdp"
	pwsv1 "github.com/trendvidia/protocompile/gen/protowire/schema/v1"
	"github.com/trendvidia/protocompile/incremental"
	"github.com/trendvidia/protocompile/incremental/queries"
	"github.com/trendvidia/protocompile/ir"
	"github.com/trendvidia/protocompile/report"
	"github.com/trendvidia/protocompile/source"
)

// requireNoErrors fails the test if the compiler emitted anything at
// Error level or worse.
//
// [report.Level] counts down from [report.ICE], so error-or-worse is
// `<= report.Error`; the other comparison quietly promotes every warning
// and remark to a test failure while letting an ICE through. A nil
// report is a failure, not a pass: it is the one state indistinguishable
// from a clean compile, and treating it as one silently restores the
// hole this helper exists to close.
func requireNoErrors(t *testing.T, rep *report.Report) {
	t.Helper()
	require.NotNil(t, rep, "incremental.Run returned no report")
	var msgs []string
	for _, d := range rep.Diagnostics {
		if d.Level() <= report.Error {
			msgs = append(msgs, d.Message())
		}
	}
	require.Empty(t, msgs, "source does not compile cleanly:\n%s", strings.Join(msgs, "\n"))
}

// compileForFDPTest compiles a single .proto source and returns the
// resulting FileDescriptorProto, ready for extension inspection.
func compileForFDPTest(t *testing.T, src string) *descriptorpb.FileDescriptorProto {
	t.Helper()
	opener := source.NewMap(map[string]*source.File{
		"x.proto": source.NewFile("x.proto", src),
	})
	allOpeners := &source.Openers{opener, source.WKTs()}

	exec := incremental.New()
	sess := new(ir.Session)
	results, rep, err := incremental.Run(t.Context(), exec, queries.IR{
		Opener:  allOpeners,
		Session: sess,
		Path:    "x.proto",
	})
	require.NoError(t, err)

	// incremental.Run reports semantic errors through the report, not
	// through err — a file the compiler rejects still yields an IR and a
	// descriptor. Discarding the report here made every test in this
	// package able to assert on carrier output for source that does not
	// compile, which is how the non-bug in #153 came to be filed.
	requireNoErrors(t, rep)

	require.Len(t, results, 1)
	require.NotNil(t, results[0].Value)

	out, err := fdp.DescriptorProto(results[0].Value)
	require.NoError(t, err)
	return out
}

// TestAnnotationEmissionScalars verifies B4 of the PSE annotation
// work: each scalar-typed argument lowers into the right oneof
// variant on AnnotationArg, and the AnnotationList extension shows
// up on the carrier's Options.
func TestAnnotationEmissionScalars(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

annotation tag(name: string, version: int32, ratio: double, enabled: bool);

@tag("alpha", 42, 0.5, true)
message M {}
`

	f := compileForFDPTest(t, src)
	require.Len(t, f.GetMessageType(), 1)
	mdp := f.GetMessageType()[0]
	require.NotNil(t, mdp.Options, "message M should have an Options message")

	list, ok := proto.GetExtension(mdp.Options, pwsv1.E_MessageAnnotations).(*pwsv1.AnnotationList)
	require.True(t, ok)
	require.NotNil(t, list)
	require.Len(t, list.Entries, 1)
	entry := list.Entries[0]
	assert.Equal(t, "test.tag", entry.Name)
	require.Len(t, entry.Args, 4)

	assert.Equal(t, "alpha", entry.Args[0].GetStringValue())
	assert.Equal(t, int64(42), entry.Args[1].GetIntValue())
	assert.InDelta(t, 0.5, entry.Args[2].GetDoubleValue(), 1e-9)
	assert.True(t, entry.Args[3].GetBoolValue())
}

// TestAnnotationEmissionExpression verifies the `expression` pseudo-
// type lowers into Expression.source with the verbatim text.
func TestAnnotationEmissionExpression(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

annotation rule_only(rule: expression);

@rule_only("this != 0")
message M {}
`

	f := compileForFDPTest(t, src)
	mdp := f.GetMessageType()[0]
	require.NotNil(t, mdp.Options)
	list, ok := proto.GetExtension(mdp.Options, pwsv1.E_MessageAnnotations).(*pwsv1.AnnotationList)
	require.True(t, ok)
	require.Len(t, list.Entries, 1)
	args := list.Entries[0].Args
	require.Len(t, args, 1)
	expr := args[0].GetExpression()
	require.NotNil(t, expr)
	// The verbatim source includes the surrounding quotes since the
	// expression captures the argument span as written.
	assert.Contains(t, expr.Source, "this != 0")
}

// TestAnnotationEmissionPath verifies enum-value reference args
// lower into a linker-resolved Literal.enum_value: enum type FQN,
// value name, and number (RFC-001 §8.1, "enum references are
// lowered resolved").
func TestAnnotationEmissionPath(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

enum OrderStatus {
  ORDER_STATUS_UNSPECIFIED = 0;
  PLACED = 1;
  SHIPPED = 2;
  CANCELLED = 3;
}

annotation sample(value: any);

@sample(OrderStatus.CANCELLED)
message M {}
`

	f := compileForFDPTest(t, src)
	mdp := f.GetMessageType()[0]
	list, ok := proto.GetExtension(mdp.Options, pwsv1.E_MessageAnnotations).(*pwsv1.AnnotationList)
	require.True(t, ok)
	require.Len(t, list.Entries, 1)
	args := list.Entries[0].Args
	require.Len(t, args, 1)
	lit := args[0].GetLiteral()
	require.NotNil(t, lit)
	ev := lit.GetEnumValue()
	require.NotNil(t, ev)
	assert.Equal(t, "test.OrderStatus", ev.GetEnumType())
	assert.Equal(t, "CANCELLED", ev.GetValueName())
	assert.Equal(t, int32(3), ev.GetNumber())
}

// TestAnnotationEmissionExpressionCalls verifies RFC-001 §8.1
// expression lowering: the capture goes into Expression.source
// verbatim (trimmed, quotes intact), calls to declared functions are
// extracted with their arity, and engine builtins are absent.
func TestAnnotationEmissionExpressionCalls(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

function in_region(value: string, regions: string);

annotation validate(rule: expression, code: string = "");

message Account {
  string country = 1
    @validate(in_region(this, ["US", "CA"]), code = "account.bad_region");
  string display_name = 2
    @validate(matches(this, "^[a-z),]+$"), code = "account.bad_name");
  int32 tier = 3
    @validate(this == 1 || this == 2, code = "account.bad_tier");
  string tags_csv = 4
    @validate(size(split(this, ",")[0]) > 0);
}
`

	f := compileForFDPTest(t, src)
	fields := f.GetMessageType()[0].GetField()
	require.Len(t, fields, 4)

	annotationOf := func(i int) *pwsv1.Annotation {
		list, ok := proto.GetExtension(fields[i].Options, pwsv1.E_FieldAnnotations).(*pwsv1.AnnotationList)
		require.True(t, ok, "field %d should carry the annotations extension", i)
		require.Len(t, list.Entries, 1)
		return list.Entries[0]
	}

	// country: declared-function call, nested list literal. The comma
	// inside the list does not count toward the call's arity.
	entry := annotationOf(0)
	require.Len(t, entry.Args, 2)
	expr := entry.Args[0].GetExpression()
	require.NotNil(t, expr)
	assert.Equal(t, `in_region(this, ["US", "CA"])`, expr.Source)
	require.Len(t, expr.Calls, 1)
	assert.Equal(t, "test.in_region", expr.Calls[0].Fqn)
	assert.Equal(t, int32(2), expr.Calls[0].Arity)
	assert.Equal(t, "code", entry.Args[1].Name)
	assert.Equal(t, "account.bad_region", entry.Args[1].GetStringValue())

	// display_name: engine builtin only; the `)` and `,` inside the
	// string literal are opaque to the capture scan.
	expr = annotationOf(1).Args[0].GetExpression()
	require.NotNil(t, expr)
	assert.Equal(t, `matches(this, "^[a-z),]+$")`, expr.Source)
	assert.Empty(t, expr.Calls)

	// tier: infix fragment with == and ||; not a named argument.
	entry = annotationOf(2)
	expr = entry.Args[0].GetExpression()
	require.NotNil(t, expr)
	assert.Equal(t, `this == 1 || this == 2`, expr.Source)
	assert.Empty(t, expr.Calls)
	assert.Equal(t, "account.bad_tier", entry.Args[1].GetStringValue())

	// tags_csv: nested delimiters of all three kinds in one capture.
	expr = annotationOf(3).Args[0].GetExpression()
	require.NotNil(t, expr)
	assert.Equal(t, `size(split(this, ",")[0]) > 0`, expr.Source)
	assert.Empty(t, expr.Calls)
}

// TestAnnotationEmissionListLiteral verifies list-literal args lower
// into Literal.list with LiteralValue elements (no names, scalar
// oneof mirroring AnnotationArg fields 10-14).
func TestAnnotationEmissionListLiteral(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

annotation allowed(values: any);

@allowed(["US", "CA", "GB"])
message M {}
`

	f := compileForFDPTest(t, src)
	mdp := f.GetMessageType()[0]
	list, ok := proto.GetExtension(mdp.Options, pwsv1.E_MessageAnnotations).(*pwsv1.AnnotationList)
	require.True(t, ok)
	require.Len(t, list.Entries, 1)
	args := list.Entries[0].Args
	require.Len(t, args, 1)
	ll := args[0].GetLiteral().GetList()
	require.NotNil(t, ll)
	require.Len(t, ll.Elements, 3)
	assert.Equal(t, "US", ll.Elements[0].GetStringValue())
	assert.Equal(t, "CA", ll.Elements[1].GetStringValue())
	assert.Equal(t, "GB", ll.Elements[2].GetStringValue())
}

// TestAnnotationEmissionTrailingPlacement verifies trailing
// annotations on fields and enum values (RFC-001 §5.1 hybrid
// placement) reach the respective Options extensions.
func TestAnnotationEmissionTrailingPlacement(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

annotation required;
annotation description(text: string);

message User {
  string display_name = 1 @required;
}

enum Tier {
  TIER_UNSPECIFIED = 0 @description("not yet selected");
}
`

	f := compileForFDPTest(t, src)

	field := f.GetMessageType()[0].GetField()[0]
	list, ok := proto.GetExtension(field.Options, pwsv1.E_FieldAnnotations).(*pwsv1.AnnotationList)
	require.True(t, ok, "field should carry the annotations extension")
	require.Len(t, list.Entries, 1)
	assert.Equal(t, "test.required", list.Entries[0].Name)

	ev := f.GetEnumType()[0].GetValue()[0]
	list, ok = proto.GetExtension(ev.Options, pwsv1.E_EnumValueAnnotations).(*pwsv1.AnnotationList)
	require.True(t, ok, "enum value should carry the annotations extension")
	require.Len(t, list.Entries, 1)
	assert.Equal(t, "test.description", list.Entries[0].Name)
	require.Len(t, list.Entries[0].Args, 1)
	assert.Equal(t, "not yet selected", list.Entries[0].Args[0].GetStringValue())
}

// TestAnnotationEmissionNegative verifies `-N` numeric negation
// produces a negated int_value.
func TestAnnotationEmissionNegative(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

annotation offset(n: int32);

@offset(-7)
message M {}
`

	f := compileForFDPTest(t, src)
	mdp := f.GetMessageType()[0]
	list, ok := proto.GetExtension(mdp.Options, pwsv1.E_MessageAnnotations).(*pwsv1.AnnotationList)
	require.True(t, ok)
	require.Len(t, list.Entries, 1)
	args := list.Entries[0].Args
	require.Len(t, args, 1)
	assert.Equal(t, int64(-7), args[0].GetIntValue())
}

// TestAnnotationEmissionFileDecls verifies the FileAnnotationDecls
// extension on FileOptions captures every `annotation` declaration
// in the file, with the correct param type classification per B3.
func TestAnnotationEmissionFileDecls(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

message Email {}

annotation required;
annotation description(text: string);
annotation validate(rule: expression, code: string);
annotation example(value: any);
annotation contact(via: Email);
`

	f := compileForFDPTest(t, src)
	require.NotNil(t, f.Options, "file should carry an Options message for the decls extension")

	decls, ok := proto.GetExtension(f.Options, pwsv1.E_AnnotationDecls).(*pwsv1.FileAnnotationDecls)
	require.True(t, ok)
	require.NotNil(t, decls)

	byName := map[string]*pwsv1.AnnotationDecl{}
	for _, d := range decls.Declarations {
		byName[d.Name] = d
	}
	assert.Len(t, byName, 5)

	// `required` is parameterless.
	if d, ok := byName["test.required"]; assert.True(t, ok, "required missing") {
		assert.Empty(t, d.Params)
	}

	// `description(text: string)` → STRING.
	if d, ok := byName["test.description"]; assert.True(t, ok, "description missing") {
		require.Len(t, d.Params, 1)
		assert.Equal(t, "text", d.Params[0].Name)
		assert.Equal(t, pwsv1.ParamType_STRING, d.Params[0].Type)
	}

	// `validate(rule: expression, code: string)` → EXPRESSION + STRING.
	if d, ok := byName["test.validate"]; assert.True(t, ok, "validate missing") {
		require.Len(t, d.Params, 2)
		assert.Equal(t, "rule", d.Params[0].Name)
		assert.Equal(t, pwsv1.ParamType_EXPRESSION, d.Params[0].Type)
		assert.Equal(t, "code", d.Params[1].Name)
		assert.Equal(t, pwsv1.ParamType_STRING, d.Params[1].Type)
	}

	// `example(value: any)` → ANY.
	if d, ok := byName["test.example"]; assert.True(t, ok, "example missing") {
		require.Len(t, d.Params, 1)
		assert.Equal(t, pwsv1.ParamType_ANY, d.Params[0].Type)
	}

	// `contact(via: Email)` → ENUM_OR_MESSAGE with type_fqn populated.
	if d, ok := byName["test.contact"]; assert.True(t, ok, "contact missing") {
		require.Len(t, d.Params, 1)
		assert.Equal(t, pwsv1.ParamType_ENUM_OR_MESSAGE, d.Params[0].Type)
		assert.Equal(t, "test.Email", d.Params[0].TypeFqn)
	}
}

// TestAnnotationEmissionParamDefaults verifies that an annotation
// declaration with default-value expressions lowers each into the
// matching AnnotationArg oneof variant on AnnotationParam.default_value.
func TestAnnotationEmissionParamDefaults(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

annotation tag(
  name: string = "anonymous",
  count: int32 = -1,
  ratio: double = 0.25,
  flag: bool = true
);
`

	f := compileForFDPTest(t, src)
	require.NotNil(t, f.Options)
	decls, ok := proto.GetExtension(f.Options, pwsv1.E_AnnotationDecls).(*pwsv1.FileAnnotationDecls)
	require.True(t, ok)
	require.Len(t, decls.Declarations, 1)
	params := decls.Declarations[0].Params
	require.Len(t, params, 4)

	assert.Equal(t, "anonymous", params[0].DefaultValue.GetStringValue())
	assert.Equal(t, int64(-1), params[1].DefaultValue.GetIntValue())
	assert.InDelta(t, 0.25, params[2].DefaultValue.GetDoubleValue(), 1e-9)
	assert.True(t, params[3].DefaultValue.GetBoolValue())
}

// TestAnnotationEmissionFileFunctions verifies the FileFunctions
// extension on FileOptions captures every `function` declaration
// in the file, preserving parameter names and textual types.
func TestAnnotationEmissionFileFunctions(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

function is_e164();
function matches(value: string, pattern: string);
`

	f := compileForFDPTest(t, src)
	require.NotNil(t, f.Options)
	fns, ok := proto.GetExtension(f.Options, pwsv1.E_Functions).(*pwsv1.FileFunctions)
	require.True(t, ok)
	require.Len(t, fns.Declarations, 2)

	assert.Equal(t, "test.is_e164", fns.Declarations[0].Name)
	assert.Empty(t, fns.Declarations[0].Params)

	assert.Equal(t, "test.matches", fns.Declarations[1].Name)
	require.Len(t, fns.Declarations[1].Params, 2)
	assert.Equal(t, "value", fns.Declarations[1].Params[0].Name)
	assert.Equal(t, "string", fns.Declarations[1].Params[0].Type)
	assert.Equal(t, "pattern", fns.Declarations[1].Params[1].Name)
	assert.Equal(t, "string", fns.Declarations[1].Params[1].Type)
}

// TestAnnotationEmissionFunctionOptions verifies that bracket-form
// options on `function` declarations land in FunctionDecl.options,
// keyed by the unqualified option name with AnnotationArg-shaped
// values routed by the value's own spelling (issue #118).
func TestAnnotationEmissionFunctionOptions(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

function matches(value: string, pattern: string)
  [error_code = "common.matches.failed"];

function tuned()
  [threshold = 0.5, max_items = 3, offset = -2, strict = true, lax = false, mode = LENIENT];

function plain();
`

	f := compileForFDPTest(t, src)
	require.NotNil(t, f.Options)
	fns, ok := proto.GetExtension(f.Options, pwsv1.E_Functions).(*pwsv1.FileFunctions)
	require.True(t, ok)
	require.Len(t, fns.Declarations, 3)

	matches := fns.Declarations[0]
	require.Len(t, matches.Options, 1)
	assert.Equal(t, "common.matches.failed", matches.Options["error_code"].GetStringValue())

	tuned := fns.Declarations[1]
	require.Len(t, tuned.Options, 6)
	assert.InDelta(t, 0.5, tuned.Options["threshold"].GetDoubleValue(), 1e-9)
	assert.Equal(t, int64(3), tuned.Options["max_items"].GetIntValue())
	assert.Equal(t, int64(-2), tuned.Options["offset"].GetIntValue())
	assert.True(t, tuned.Options["strict"].GetBoolValue())
	if lax, ok := tuned.Options["lax"]; assert.True(t, ok) {
		assert.False(t, lax.GetBoolValue())
	}
	// No annotation scope exists to resolve enum-value references
	// against, so the reference is carried verbatim.
	mode := tuned.Options["mode"].GetLiteral().GetEnumValue()
	require.NotNil(t, mode)
	assert.Equal(t, "LENIENT", mode.GetValueName())
	assert.Empty(t, mode.GetEnumType())

	assert.Empty(t, fns.Declarations[2].Options)
}

// TestAnnotationEmissionFileTypeDecls verifies that `type` alias
// declarations land in the FileTypeDecls extension on FileOptions,
// with the base type fully qualified (the base_type_fqn contract:
// consumers get resolution-free reads even for bases written bare
// in source) and any trailing annotations preserved.
func TestAnnotationEmissionFileTypeDecls(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

annotation since(when: string);

message Address {}

enum Status {
  STATUS_UNSPECIFIED = 0;
}

type Email = string;
type Phone = string @since("2026-06-01");
type Loc = Address;
type Settled = Status;
type Chain = Email;
`

	f := compileForFDPTest(t, src)
	require.NotNil(t, f.Options)
	decls, ok := proto.GetExtension(f.Options, pwsv1.E_TypeDecls).(*pwsv1.FileTypeDecls)
	require.True(t, ok)
	require.Len(t, decls.Declarations, 5)

	byName := map[string]*pwsv1.TypeDecl{}
	for _, d := range decls.Declarations {
		byName[d.Name] = d
	}

	if d, ok := byName["test.Email"]; assert.True(t, ok) {
		assert.Equal(t, "string", d.BaseTypeFqn)
		assert.Nil(t, d.Annotations, "Email has no trailing annotations")
	}
	if d, ok := byName["test.Loc"]; assert.True(t, ok) {
		assert.Equal(t, "test.Address", d.BaseTypeFqn,
			"bare in-package message base must lower fully qualified")
	}
	if d, ok := byName["test.Settled"]; assert.True(t, ok) {
		assert.Equal(t, "test.Status", d.BaseTypeFqn,
			"bare in-package enum base must lower fully qualified")
	}
	if d, ok := byName["test.Chain"]; assert.True(t, ok) {
		assert.Equal(t, "test.Email", d.BaseTypeFqn,
			"chained alias base must lower fully qualified")
	}
	if d, ok := byName["test.Phone"]; assert.True(t, ok) {
		assert.Equal(t, "string", d.BaseTypeFqn)
		require.NotNil(t, d.Annotations)
		require.Len(t, d.Annotations.Entries, 1)
		entry := d.Annotations.Entries[0]
		assert.Equal(t, "test.since", entry.Name)
		require.Len(t, entry.Args, 1)
		assert.Equal(t, "2026-06-01", entry.Args[0].GetStringValue())
	}
}

// TestAnnotationEmissionCarrierCoverage verifies the extension lands
// on the correct Options message for each carrier kind.
func TestAnnotationEmissionCarrierCoverage(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

annotation k;

@k
message M {
  string field_a = 1 @k;
  @k
  oneof choice {
    string field_b = 2;
  }
}

@k
enum E {
  E_UNSET = 0;
  E_ONE = 1 @k;
}

@k
service S {
  @k
  rpc Ping(M) returns (M);
}
`

	f := compileForFDPTest(t, src)
	require.Len(t, f.GetMessageType(), 1)
	mdp := f.GetMessageType()[0]
	assert.NotNil(t, proto.GetExtension(mdp.Options, pwsv1.E_MessageAnnotations))

	require.Len(t, mdp.GetField(), 2)
	// field_a is the first member.
	assert.NotNil(t, proto.GetExtension(mdp.GetField()[0].Options, pwsv1.E_FieldAnnotations))

	require.Len(t, mdp.GetOneofDecl(), 1)
	assert.NotNil(t, proto.GetExtension(mdp.GetOneofDecl()[0].Options, pwsv1.E_OneofAnnotations))

	require.Len(t, f.GetEnumType(), 1)
	edp := f.GetEnumType()[0]
	assert.NotNil(t, proto.GetExtension(edp.Options, pwsv1.E_EnumAnnotations))
	require.Len(t, edp.GetValue(), 2)
	// E_ONE is the second value (E_UNSET first).
	assert.NotNil(t, proto.GetExtension(edp.GetValue()[1].Options, pwsv1.E_EnumValueAnnotations))

	require.Len(t, f.GetService(), 1)
	sdp := f.GetService()[0]
	assert.NotNil(t, proto.GetExtension(sdp.Options, pwsv1.E_ServiceAnnotations))
	require.Len(t, sdp.GetMethod(), 1)
	assert.NotNil(t, proto.GetExtension(sdp.GetMethod()[0].Options, pwsv1.E_MethodAnnotations))
}

// TestAnnotationEmissionMessageLiteral verifies message-literal args
// lower into Literal.message: a google.protobuf.Any typed by the
// literal's resolved message and serialized at lowering. Mirrors the
// three-entry shape of protowire's 11_literal_carrier_golden.textproto
// (resolved enum, Any message, list) built from 10_literal_args.proto's
// source forms.
func TestAnnotationEmissionMessageLiteral(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package fixtures.literals;

annotation sample(value: any);
annotation allowed(values: any);

enum OrderStatus {
  ORDER_STATUS_UNSPECIFIED = 0;
  PLACED = 1;
  SHIPPED = 2;
  CANCELLED = 3;
}

message Money {
  string currency = 1;
  int64 units = 2;
}

message Order {
  OrderStatus status = 1
    @sample(OrderStatus.CANCELLED);
  Money total = 2
    @sample(Money{currency: "USD", units: 5});
  string country = 3
    @allowed(["US", "CA", "GB"]);
}
`

	f := compileForFDPTest(t, src)
	fields := f.GetMessageType()[1].GetField()
	require.Len(t, fields, 3)

	argOf := func(i int) *pwsv1.AnnotationArg {
		list, ok := proto.GetExtension(fields[i].Options, pwsv1.E_FieldAnnotations).(*pwsv1.AnnotationList)
		require.True(t, ok)
		require.Len(t, list.Entries, 1)
		require.Len(t, list.Entries[0].Args, 1)
		return list.Entries[0].Args[0]
	}

	// Entry 1: linker-resolved enum-value reference.
	ev := argOf(0).GetLiteral().GetEnumValue()
	require.NotNil(t, ev)
	assert.Equal(t, "fixtures.literals.OrderStatus", ev.GetEnumType())
	assert.Equal(t, "CANCELLED", ev.GetValueName())
	assert.Equal(t, int32(3), ev.GetNumber())

	// Entry 2: typed message literal, serialized into an Any at
	// lowering. The golden's wire bytes are "\n\003USD\020\005".
	anyMsg := argOf(1).GetLiteral().GetMessage()
	require.NotNil(t, anyMsg)
	assert.Equal(t, "type.googleapis.com/fixtures.literals.Money", anyMsg.GetTypeUrl())
	assert.Equal(t, []byte("\n\x03USD\x10\x05"), anyMsg.GetValue())

	// Entry 3: homogeneous list literal.
	ll := argOf(2).GetLiteral().GetList()
	require.NotNil(t, ll)
	require.Len(t, ll.Elements, 3)
	assert.Equal(t, "US", ll.Elements[0].GetStringValue())
}

// TestAnnotationEmissionMessageLiteralOnMessageParam verifies the
// declared-type path: a message-typed parameter takes an untyped
// literal (the type comes from the declaration) and an explicitly
// typed one that must match.
func TestAnnotationEmissionMessageLiteralOnMessageParam(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

message Money {
  string currency = 1;
}

annotation price(value: Money);

@price({currency: "EUR"})
message A {}

@price(Money{currency: "GBP"})
message B {}
`

	f := compileForFDPTest(t, src)
	for i, want := range []string{"EUR", "GBP"} {
		mdp := f.GetMessageType()[i+1]
		list, ok := proto.GetExtension(mdp.Options, pwsv1.E_MessageAnnotations).(*pwsv1.AnnotationList)
		require.True(t, ok)
		require.Len(t, list.Entries, 1)
		anyMsg := list.Entries[0].Args[0].GetLiteral().GetMessage()
		require.NotNil(t, anyMsg)
		assert.Equal(t, "type.googleapis.com/test.Money", anyMsg.GetTypeUrl())
		assert.Equal(t, append([]byte("\n\x03"), want...), anyMsg.GetValue())
	}
}

// TestAnnotationEmissionLocations verifies Annotation.location (the
// @ token's position) and Expression.location (the capture's opening
// position) are populated, and that alias-propagated uses point back
// to the alias's defining file.
func TestAnnotationEmissionLocations(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

annotation validate(rule: expression, code: string = "");

message M {
  string s = 1
    @validate(this.size() > 0, code = "s.empty");
}
`

	f := compileForFDPTest(t, src)
	field := f.GetMessageType()[0].GetField()[0]
	list, ok := proto.GetExtension(field.Options, pwsv1.E_FieldAnnotations).(*pwsv1.AnnotationList)
	require.True(t, ok)
	require.Len(t, list.Entries, 1)
	entry := list.Entries[0]

	// The @ token sits on line 8, column 5.
	require.NotNil(t, entry.Location)
	assert.Equal(t, "x.proto", entry.Location.GetFile())
	assert.Equal(t, int32(8), entry.Location.GetLine())
	assert.Equal(t, int32(5), entry.Location.GetColumn())

	// The expression capture opens right after `@validate(`.
	expr := entry.Args[0].GetExpression()
	require.NotNil(t, expr)
	require.NotNil(t, expr.Location)
	assert.Equal(t, "x.proto", expr.Location.GetFile())
	assert.Equal(t, int32(8), expr.Location.GetLine())
	assert.Equal(t, int32(15), expr.Location.GetColumn())
}

// TestAnnotationEmissionDeclLocations verifies the file-scope
// declaration carriers record each declaration's source location,
// anchored at the declaration-name token (matching the use-site
// convention of pointing at the name).
func TestAnnotationEmissionDeclLocations(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

annotation since(when: string);

function is_e164();

type Email = string;
`

	f := compileForFDPTest(t, src)
	require.NotNil(t, f.Options)

	anns, ok := proto.GetExtension(f.Options, pwsv1.E_AnnotationDecls).(*pwsv1.FileAnnotationDecls)
	require.True(t, ok)
	require.Len(t, anns.Declarations, 1)
	loc := anns.Declarations[0].Location
	require.NotNil(t, loc, "AnnotationDecl.location should be populated")
	assert.Equal(t, "x.proto", loc.GetFile())
	assert.Equal(t, int32(4), loc.GetLine())
	assert.Equal(t, int32(12), loc.GetColumn(), "anchored at the `since` name token")

	fns, ok := proto.GetExtension(f.Options, pwsv1.E_Functions).(*pwsv1.FileFunctions)
	require.True(t, ok)
	require.Len(t, fns.Declarations, 1)
	loc = fns.Declarations[0].Location
	require.NotNil(t, loc, "FunctionDecl.location should be populated")
	assert.Equal(t, "x.proto", loc.GetFile())
	assert.Equal(t, int32(6), loc.GetLine())
	assert.Equal(t, int32(10), loc.GetColumn(), "anchored at the `is_e164` name token")

	types, ok := proto.GetExtension(f.Options, pwsv1.E_TypeDecls).(*pwsv1.FileTypeDecls)
	require.True(t, ok)
	require.Len(t, types.Declarations, 1)
	loc = types.Declarations[0].Location
	require.NotNil(t, loc, "TypeDecl.location should be populated")
	assert.Equal(t, "x.proto", loc.GetFile())
	assert.Equal(t, int32(8), loc.GetLine())
	assert.Equal(t, int32(6), loc.GetColumn(), "anchored at the `Email` name token")
}

// TestAnnotationEmissionLocationCrossFile verifies an
// alias-propagated use's location points into the alias's defining
// file, not the consuming one.
func TestAnnotationEmissionLocationCrossFile(t *testing.T) {
	t.Parallel()

	const typesSrc = `syntax = "proto3";
package types;

annotation validate(rule: expression);

type Email = string @validate(this.size() > 0);
`
	const userSrc = `syntax = "proto3";
package app;

import "types.proto";

message User {
  types.Email email = 1;
}
`
	opener := source.NewMap(map[string]*source.File{
		"types.proto": source.NewFile("types.proto", typesSrc),
		"user.proto":  source.NewFile("user.proto", userSrc),
	})
	allOpeners := &source.Openers{opener, source.WKTs()}

	exec := incremental.New()
	sess := new(ir.Session)
	results, rep, err := incremental.Run(t.Context(), exec, queries.IR{
		Opener:  allOpeners,
		Session: sess,
		Path:    "user.proto",
	})
	require.NoError(t, err)
	requireNoErrors(t, rep)
	require.NotNil(t, results[0].Value)

	out, err := fdp.DescriptorProto(results[0].Value)
	require.NoError(t, err)

	field := out.GetMessageType()[0].GetField()[0]
	list, ok := proto.GetExtension(field.Options, pwsv1.E_FieldAnnotations).(*pwsv1.AnnotationList)
	require.True(t, ok)
	require.Len(t, list.Entries, 1)
	entry := list.Entries[0]

	require.NotNil(t, entry.Location)
	assert.Equal(t, "types.proto", entry.Location.GetFile(),
		"propagated use's @ token lives in the alias's defining file")
	require.NotNil(t, entry.Args[0].GetExpression().Location)
	assert.Equal(t, "types.proto", entry.Args[0].GetExpression().Location.GetFile())
}

// TestAnnotationEmissionDefaultEnumRef verifies enum-value defaults
// lower resolved into AnnotationParam.default_value, per the same
// EnumLiteral rules as use-site arguments.
func TestAnnotationEmissionDefaultEnumRef(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

enum Tier {
  TIER_UNSPECIFIED = 0;
  TIER_FREE = 1;
}

annotation plan(t: Tier = Tier.TIER_FREE);
`

	f := compileForFDPTest(t, src)
	require.NotNil(t, f.Options)
	decls, ok := proto.GetExtension(f.Options, pwsv1.E_AnnotationDecls).(*pwsv1.FileAnnotationDecls)
	require.True(t, ok)
	require.Len(t, decls.Declarations, 1)
	require.Len(t, decls.Declarations[0].Params, 1)

	deflt := decls.Declarations[0].Params[0].DefaultValue
	require.NotNil(t, deflt)
	ev := deflt.GetLiteral().GetEnumValue()
	require.NotNil(t, ev)
	assert.Equal(t, "test.Tier", ev.GetEnumType())
	assert.Equal(t, "TIER_FREE", ev.GetValueName())
	assert.Equal(t, int32(1), ev.GetNumber())
}

// TestAnnotationEmissionMessageListElements verifies the lowered
// carrier shape for message literals in list-element position
// (RFC-001 §8.1, LiteralValue.literal): each element serializes as a
// google.protobuf.Any inside ListLiteral.elements, including in
// nested lists.
func TestAnnotationEmissionMessageListElements(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

annotation meta(value: any);

message Item { int32 status = 1; }

@meta([Item{status: 1}, Item{status: 2}])
message Flat {}

@meta([[Item{status: 3}]])
message Nested {}
`

	f := compileForFDPTest(t, src)

	argOf := func(i int) *pwsv1.AnnotationArg {
		list, ok := proto.GetExtension(f.GetMessageType()[i].Options, pwsv1.E_MessageAnnotations).(*pwsv1.AnnotationList)
		require.True(t, ok)
		require.Len(t, list.Entries, 1)
		require.Len(t, list.Entries[0].Args, 1)
		return list.Entries[0].Args[0]
	}

	// Flat: [Item{status: 1}, Item{status: 2}] — two message
	// elements, each an Any serialized at lowering. Item{status: N}
	// is field 1 varint: "\x08" N.
	flat := argOf(1).GetLiteral().GetList()
	require.NotNil(t, flat)
	require.Len(t, flat.Elements, 2)
	for i, elem := range flat.Elements {
		anyMsg := elem.GetLiteral().GetMessage()
		require.NotNil(t, anyMsg, "element %d should be a message literal", i)
		assert.Equal(t, "type.googleapis.com/test.Item", anyMsg.GetTypeUrl())
		assert.Equal(t, []byte{0x08, byte(i + 1)}, anyMsg.GetValue())
	}

	// Nested: [[Item{status: 3}]] — a list element that is itself a
	// list whose single element is the message Any.
	nested := argOf(2).GetLiteral().GetList()
	require.NotNil(t, nested)
	require.Len(t, nested.Elements, 1)
	inner := nested.Elements[0].GetLiteral().GetList()
	require.NotNil(t, inner)
	require.Len(t, inner.Elements, 1)
	anyMsg := inner.Elements[0].GetLiteral().GetMessage()
	require.NotNil(t, anyMsg)
	assert.Equal(t, "type.googleapis.com/test.Item", anyMsg.GetTypeUrl())
	assert.Equal(t, []byte{0x08, 0x03}, anyMsg.GetValue())
}

// annotationArg compiles src and returns the single argument of the
// single annotation on message M.
func annotationArg(t *testing.T, src string) *pwsv1.AnnotationArg {
	t.Helper()
	f := compileForFDPTest(t, src)
	require.Len(t, f.GetMessageType(), 1)
	mdp := f.GetMessageType()[0]
	require.NotNil(t, mdp.Options, "message M should have an Options message")
	list, ok := proto.GetExtension(mdp.Options, pwsv1.E_MessageAnnotations).(*pwsv1.AnnotationList)
	require.True(t, ok)
	require.NotNil(t, list)
	require.Len(t, list.Entries, 1)
	require.Len(t, list.Entries[0].Args, 1)
	return list.Entries[0].Args[0]
}

// TestAnnotationEnumParamArgLowering pins what an *accepted* argument
// on an enum-typed parameter lowers to.
//
// Removing the two enum rows from TestAnnotationNumericRouting removed
// the only fdp coverage of that path: the replacement pin in ir asserts
// that the rejected spellings are diagnosed, which says nothing about
// what the accepted ones emit. A regression that made `@e(GREEN)`
// compile cleanly but emit no arg, or an int_value, would leave both
// packages green. Neither of the neighbouring enum pins covers it —
// TestAnnotationEmissionPath goes through an `any` parameter and
// TestAnnotationParamDefaultEmission through a default value, so
// validateUseEnumArg is on neither path.
//
// RED is here because it is the enum's zero value: a lowering that
// treated "no number" and "number 0" alike would still pass on GREEN.
func TestAnnotationEnumParamArgLowering(t *testing.T) {
	t.Parallel()

	const tmpl = `syntax = "proto3";
package test;

enum Color { RED = 0; GREEN = 1; }

annotation e(value: Color);

@e(%s)
message M {}
`

	for _, tc := range []struct {
		name, use, wantValue string
		wantNumber           int32
	}{
		{name: "bare", use: "GREEN", wantValue: "GREEN", wantNumber: 1},
		{name: "qualified", use: "Color.GREEN", wantValue: "GREEN", wantNumber: 1},
		{name: "zero_value", use: "RED", wantValue: "RED", wantNumber: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			arg := annotationArg(t, fmt.Sprintf(tmpl, tc.use))
			lit := arg.GetLiteral()
			require.NotNil(t, lit, "an enum argument lowers to a Literal")
			ev := lit.GetEnumValue()
			require.NotNil(t, ev, "an enum argument lowers to Literal.enum_value")
			assert.Equal(t, "test.Color", ev.GetEnumType())
			assert.Equal(t, tc.wantValue, ev.GetValueName())
			assert.Equal(t, tc.wantNumber, ev.GetNumber())
		})
	}
}

// TestAnnotationNumericRouting pins how a numeric literal is routed
// into AnnotationArg for each kind of parameter that can receive one.
//
// An enum-typed parameter has no row here on purpose: RFC-001 §5.1
// rule 4 gives it a `qualifiedIdent`, so every scalar literal on one is
// a compile error rather than a lowering. That is pinned as a
// diagnostic in ir, which is where it is raised.
//
// The routes are pinned together deliberately. They are branches of a
// single condition in buildLiteralArg, and issue #149 was one of them
// drifting away from the others: an `any` parameter is neither a
// declared float scalar nor a zero parameter, so a float literal fell
// through to the integer lowering and `@default(1.5)` reached the
// carrier as `int_value: 1` — the fraction gone, and no diagnostic to
// tell the schema author. The canonical annotation library declares
// `default` and `example` as `any`, so this was the common case.
func TestAnnotationNumericRouting(t *testing.T) {
	t.Parallel()

	const tmpl = `syntax = "proto3";
package test;

annotation a(value: %s);

@a(%s)
message M {}
`

	tests := []struct {
		name  string
		param string // declared parameter type
		lit   string // literal at the use site
		// Exactly one of these is expected; isDouble selects which.
		isDouble   bool
		wantDouble float64
		wantInt    int64
	}{
		// `any` declares no scalar to convert towards, so the
		// literal's own spelling decides. This is #149.
		{name: "any/float", param: "any", lit: "1.5", isDouble: true, wantDouble: 1.5},
		{name: "any/float_whole", param: "any", lit: "2.0", isDouble: true, wantDouble: 2},
		{name: "any/int", param: "any", lit: "3", wantInt: 3},

		// A declared float scalar converts towards the declared type,
		// so even an integer literal lands in double_value.
		{name: "double/float", param: "double", lit: "2.25", isDouble: true, wantDouble: 2.25},
		{name: "double/int", param: "double", lit: "7", isDouble: true, wantDouble: 7},
		{name: "float/float", param: "float", lit: "0.5", isDouble: true, wantDouble: 0.5},

		// Declared integer scalars are unaffected by #149: widening
		// the untyped case must not change what a typed parameter
		// accepts.
		{name: "int32/int", param: "int32", lit: "3", wantInt: 3},
		{name: "int32/negative", param: "int32", lit: "-2", wantInt: -2},
		// NumberToken.Int returns a uint64 reinterpreted via int64, so
		// the unsigned maximum is carried as two's complement. A
		// consumer that knows the target field is unsigned recovers it
		// exactly; this is by design, not the #149 defect.
		{name: "uint64/max", param: "uint64", lit: "18446744073709551615", wantInt: -1},

		// #165. `Int` reports exactness, and it is false precisely when
		// the value does not fit — the big-integer path saturates to
		// MaxUint64 and says so. Discarding that flag wrote the saturated
		// value as if the author had asked for it.
		//
		// The pair below is the whole defect: they are ONE apart, and
		// before the fix both reached the carrier as `int_value: -1`, so
		// a consumer could not tell a literal that means MaxUint64 from
		// one that overflowed past it.
		{name: "any/uint64_max_exact", param: "any", lit: "18446744073709551615", wantInt: -1},
		{
			name: "any/uint64_max_plus_one", param: "any", lit: "18446744073709551616",
			isDouble: true, wantDouble: 18446744073709551616,
		},

		// An exponent with integer value takes the integer path —
		// IsFloat is false for it, as documented ("can only be used as a
		// float literal, even if it has integer value"). So exactness,
		// not spelling, is what separates these two.
		{name: "any/exponent_in_range", param: "any", lit: "1e10", wantInt: 10000000000},
		{name: "any/exponent_out_of_range", param: "any", lit: "1e100", isDouble: true, wantDouble: 1e100},
		// Exact, and large enough to wrap into a negative int64. Carried
		// as two's complement by design, the same as uint64/max.
		{name: "any/exponent_at_wrap", param: "any", lit: "1e19", wantInt: -8446744073709551616},
		{
			name: "any/big_int_out_of_range", param: "any", lit: "99999999999999999999999",
			isDouble: true, wantDouble: 1e23,
		},

		// A negative fraction that truncates to zero is zero, so it stays on
		// the integer route even on an unsigned parameter — the accepted
		// side of the `-0` rule in TestAnnotationScalarArgRange.
		{name: "uint32/negative_fraction_to_zero", param: "uint32", lit: "-0.5", wantInt: 0},

		// A value that does not fit a declared integer parameter is now a
		// compile error, so it has no row here — this table only covers
		// what lowers. The diagnostic is pinned in ir, where it is raised:
		// TestAnnotationScalarArgRange.
		//
		// A fractional literal that DOES fit is still lowered, truncated:
		// `@a(1.5)` on int32 gives int_value 1. That is a different
		// question from range and is deliberately untouched.
		{name: "int32/fraction_truncates", param: "int32", lit: "1.5", wantInt: 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			arg := annotationArg(t, fmt.Sprintf(tmpl, tc.param, tc.lit))
			if tc.isDouble {
				require.IsType(t, (*pwsv1.AnnotationArg_DoubleValue)(nil), arg.Value,
					"want double_value, got %v", arg.Value)
				assert.InDelta(t, tc.wantDouble, arg.GetDoubleValue(), 1e-9)
				return
			}
			require.IsType(t, (*pwsv1.AnnotationArg_IntValue)(nil), arg.Value,
				"want int_value, got %v", arg.Value)
			assert.Equal(t, tc.wantInt, arg.GetIntValue())
		})
	}
}

// TestAnnotationNumericRoutingNoParam covers the third route: a
// declaration with no parameter to type against at all. Function
// options carry AnnotationArg-shaped values with no declared
// parameter, so like `any` they follow the literal's own spelling.
// Kept next to TestAnnotationNumericRouting so the routes cannot
// drift apart unnoticed.
func TestAnnotationNumericRoutingNoParam(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

function f() [ratio = 1.5, whole = 2.0, count = 3, negative = -2];
`

	f := compileForFDPTest(t, src)
	require.NotNil(t, f.Options)
	fns, ok := proto.GetExtension(f.Options, pwsv1.E_Functions).(*pwsv1.FileFunctions)
	require.True(t, ok)
	require.Len(t, fns.Declarations, 1)
	opts := fns.Declarations[0].Options

	require.IsType(t, (*pwsv1.AnnotationArg_DoubleValue)(nil), opts["ratio"].Value)
	assert.InDelta(t, 1.5, opts["ratio"].GetDoubleValue(), 1e-9)
	require.IsType(t, (*pwsv1.AnnotationArg_DoubleValue)(nil), opts["whole"].Value)
	assert.InDelta(t, 2, opts["whole"].GetDoubleValue(), 1e-9)
	require.IsType(t, (*pwsv1.AnnotationArg_IntValue)(nil), opts["count"].Value)
	assert.Equal(t, int64(3), opts["count"].GetIntValue())
	require.IsType(t, (*pwsv1.AnnotationArg_IntValue)(nil), opts["negative"].Value)
	assert.Equal(t, int64(-2), opts["negative"].GetIntValue())
}
