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
	"math"
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

	list, _ := proto.GetExtension(mdp.Options, pwsv1.E_MessageAnnotations).(*pwsv1.AnnotationList)
	require.NotNil(t, list, "carries no message annotations extension")
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
	list, _ := proto.GetExtension(mdp.Options, pwsv1.E_MessageAnnotations).(*pwsv1.AnnotationList)
	require.NotNil(t, list, "carries no message annotations extension")
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
	list, _ := proto.GetExtension(mdp.Options, pwsv1.E_MessageAnnotations).(*pwsv1.AnnotationList)
	require.NotNil(t, list, "carries no message annotations extension")
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
		list, _ := proto.GetExtension(fields[i].Options, pwsv1.E_FieldAnnotations).(*pwsv1.AnnotationList)
		require.NotNil(t, list, "carries no field annotations extension")
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
	list, _ := proto.GetExtension(mdp.Options, pwsv1.E_MessageAnnotations).(*pwsv1.AnnotationList)
	require.NotNil(t, list, "carries no message annotations extension")
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
	list, _ := proto.GetExtension(field.Options, pwsv1.E_FieldAnnotations).(*pwsv1.AnnotationList)
	require.NotNil(t, list, "carries no field annotations extension")
	require.Len(t, list.Entries, 1)
	assert.Equal(t, "test.required", list.Entries[0].Name)

	ev := f.GetEnumType()[0].GetValue()[0]
	list, _ = proto.GetExtension(ev.Options, pwsv1.E_EnumValueAnnotations).(*pwsv1.AnnotationList)
	require.NotNil(t, list, "carries no enum value annotations extension")
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
	list, _ := proto.GetExtension(mdp.Options, pwsv1.E_MessageAnnotations).(*pwsv1.AnnotationList)
	require.NotNil(t, list, "carries no message annotations extension")
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

	decls, _ := proto.GetExtension(f.Options, pwsv1.E_AnnotationDecls).(*pwsv1.FileAnnotationDecls)
	require.NotNil(t, decls, "carries no annotation decls extension")

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
	decls, _ := proto.GetExtension(f.Options, pwsv1.E_AnnotationDecls).(*pwsv1.FileAnnotationDecls)
	require.NotNil(t, decls, "carries no annotation decls extension")
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
	fns, _ := proto.GetExtension(f.Options, pwsv1.E_Functions).(*pwsv1.FileFunctions)
	require.NotNil(t, fns, "carries no functions extension")
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
	fns, _ := proto.GetExtension(f.Options, pwsv1.E_Functions).(*pwsv1.FileFunctions)
	require.NotNil(t, fns, "carries no functions extension")
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
	decls, _ := proto.GetExtension(f.Options, pwsv1.E_TypeDecls).(*pwsv1.FileTypeDecls)
	require.NotNil(t, decls, "carries no type decls extension")
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
		list, _ := proto.GetExtension(fields[i].Options, pwsv1.E_FieldAnnotations).(*pwsv1.AnnotationList)
		require.NotNil(t, list, "carries no field annotations extension")
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
		list, _ := proto.GetExtension(mdp.Options, pwsv1.E_MessageAnnotations).(*pwsv1.AnnotationList)
		require.NotNil(t, list, "carries no message annotations extension")
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
	list, _ := proto.GetExtension(field.Options, pwsv1.E_FieldAnnotations).(*pwsv1.AnnotationList)
	require.NotNil(t, list, "carries no field annotations extension")
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

	anns, _ := proto.GetExtension(f.Options, pwsv1.E_AnnotationDecls).(*pwsv1.FileAnnotationDecls)
	require.NotNil(t, anns, "carries no annotation decls extension")
	require.Len(t, anns.Declarations, 1)
	loc := anns.Declarations[0].Location
	require.NotNil(t, loc, "AnnotationDecl.location should be populated")
	assert.Equal(t, "x.proto", loc.GetFile())
	assert.Equal(t, int32(4), loc.GetLine())
	assert.Equal(t, int32(12), loc.GetColumn(), "anchored at the `since` name token")

	fns, _ := proto.GetExtension(f.Options, pwsv1.E_Functions).(*pwsv1.FileFunctions)
	require.NotNil(t, fns, "carries no functions extension")
	require.Len(t, fns.Declarations, 1)
	loc = fns.Declarations[0].Location
	require.NotNil(t, loc, "FunctionDecl.location should be populated")
	assert.Equal(t, "x.proto", loc.GetFile())
	assert.Equal(t, int32(6), loc.GetLine())
	assert.Equal(t, int32(10), loc.GetColumn(), "anchored at the `is_e164` name token")

	types, _ := proto.GetExtension(f.Options, pwsv1.E_TypeDecls).(*pwsv1.FileTypeDecls)
	require.NotNil(t, types, "carries no type decls extension")
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
	list, _ := proto.GetExtension(field.Options, pwsv1.E_FieldAnnotations).(*pwsv1.AnnotationList)
	require.NotNil(t, list, "carries no field annotations extension")
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
	decls, _ := proto.GetExtension(f.Options, pwsv1.E_AnnotationDecls).(*pwsv1.FileAnnotationDecls)
	require.NotNil(t, decls, "carries no annotation decls extension")
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
		list, _ := proto.GetExtension(f.GetMessageType()[i].Options, pwsv1.E_MessageAnnotations).(*pwsv1.AnnotationList)
		require.NotNil(t, list, "carries no message annotations extension")
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
	list, _ := proto.GetExtension(mdp.Options, pwsv1.E_MessageAnnotations).(*pwsv1.AnnotationList)
	require.NotNil(t, list, "carries no message annotations extension")
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

		// An exponent is a FLOAT spelling, so with no target to convert to
		// these keep their own type (#188). They used to take the integer
		// path, because the lexer does not count a positive exponent as a
		// float (#191) — which is how `1e19` reached a carrier as
		// int_value: -8446744073709551616, the symptom that opened this
		// whole family in #172.
		{name: "any/exponent_in_range", param: "any", lit: "1e10", isDouble: true, wantDouble: 1e10},
		{name: "any/exponent_out_of_range", param: "any", lit: "1e100", isDouble: true, wantDouble: 1e100},
		{name: "any/exponent_at_wrap", param: "any", lit: "1e19", isDouble: true, wantDouble: 1e19},
		{
			name: "any/big_int_out_of_range", param: "any", lit: "99999999999999999999999",
			isDouble: true, wantDouble: 1e23,
		},

		// The mirror of that pair, one range down. A negative literal is
		// lowered by negating what buildLiteralArg produced, which is the
		// magnitude reinterpreted through int64 — so for a magnitude in
		// (MaxInt64, MaxUint64] the negation flipped the sign straight back
		// and `-18446744073709551615` reached the carrier as `int_value: 1`.
		// These three are also one apart at each end of int64's range.
		{name: "any/negative_int64_min", param: "any", lit: "-9223372036854775808", wantInt: math.MinInt64},
		{
			name: "any/negative_past_int64_min", param: "any", lit: "-9223372036854775809",
			isDouble: true, wantDouble: -9223372036854775809,
		},
		{
			name: "any/negative_uint64_max", param: "any", lit: "-18446744073709551615",
			isDouble: true, wantDouble: -18446744073709551615,
		},
		{
			name: "any/negative_uint64_max_plus_one", param: "any", lit: "-18446744073709551616",
			isDouble: true, wantDouble: -18446744073709551616,
		},
		{name: "any/negative_small", param: "any", lit: "-3", wantInt: -3},

		// A negative fraction that ROUNDS to zero lowers as zero. It has to
		// be a SIGNED parameter: an unsigned one rejects any negated
		// literal whatever its magnitude, matching checkIntBounds (#169).
		//
		// `-0.5` is not this case even here: half rounds away from zero
		// (#167), so it reaches a magnitude of 1.
		{name: "int32/negative_fraction_to_zero", param: "int32", lit: "-0.4", wantInt: 0},

		// A value that does not fit a declared integer parameter is now a
		// compile error, so it has no row here — this table only covers
		// what lowers. The diagnostic is pinned in ir, where it is raised:
		// TestAnnotationScalarArgRange.
		//
		// A fractional literal that DOES fit is still lowered rather than
		// diagnosed — whether a value is whole is a different question
		// from whether it fits, and only the second is checked.
		//
		// It rounds to nearest, half away from zero (#167). It used to
		// depend on whether the literal was exactly representable as a
		// float64: 1.5 truncated to 1 while 1.1 came back as 0, because
		// the two storage paths behind NumberToken.Int disagreed.
		{name: "int32/fraction_rounds", param: "int32", lit: "1.5", wantInt: 2},
		{name: "int32/fraction_rounds_down", param: "int32", lit: "1.1", wantInt: 1},
		{name: "int32/fraction_rounds_up", param: "int32", lit: "2.9", wantInt: 3},
		// Large enough that the old zero-return was unmistakable.
		{name: "int32/fraction_large", param: "int32", lit: "1000000.1", wantInt: 1000000},
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
	fns, _ := proto.GetExtension(f.Options, pwsv1.E_Functions).(*pwsv1.FileFunctions)
	require.NotNil(t, fns, "carries no functions extension")
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

// fieldAnnotationArg compiles a single field carrying `@default(lit)` and
// returns the lowered argument.
func fieldAnnotationArg(t *testing.T, fieldType, lit string) *pwsv1.AnnotationArg {
	t.Helper()
	return fieldArg(t, compileForFDPTest(t, fmt.Sprintf(`syntax = "proto3";
package test;

annotation default(value: any);

message M {
  %s f = 1 @default(%s);
}
`, fieldType, lit)))
}

// TestAnnotationUntypedArgRoutesByCarrier pins the rule issue #172 is about:
// an untyped parameter has no type of its own, so the lowering routes by the
// type of the thing the annotation is attached to.
//
// The band above MaxInt64 is why. `int_value` is an int64, so a value in
// (MaxInt64, MaxUint64] is stored two's-complement — recoverable only by a
// consumer that knows the target is unsigned. A `double` target does not,
// and applied the negative number it was handed: `@default(1e19)` on a double
// field produced -8446744073709551616.
//
// The bound is exercised from both sides against both kinds of target,
// because the whole defect lives in one bit of range.
func TestAnnotationUntypedArgRoutesByCarrier(t *testing.T) {
	t.Parallel()

	const (
		maxInt64  = "9223372036854775807"
		overInt64 = "9223372036854775808"
		maxUint64 = "18446744073709551615"
	)

	t.Run("float carrier takes double_value", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			field, lit string
			want       float64
		}{
			// The reported case, and the reason for the rule.
			{"double", "1e19", 1e19},
			{"float", "1e19", 1e19},
			{"double", overInt64, 9223372036854775808},
			{"double", maxUint64, 18446744073709551615},
			// Below the band too: the rule is the carrier's type, not the
			// value. A double field cannot hold MaxInt64 exactly anyway, so
			// the carrier now reports what the field will actually store.
			{"double", maxInt64, 9223372036854775807},
			{"double", "42", 42},
			{"double", "1.5", 1.5},
		} {
			arg := fieldAnnotationArg(t, tc.field, tc.lit)
			require.IsType(t, (*pwsv1.AnnotationArg_DoubleValue)(nil), arg.Value,
				"%s field, literal %s: want double_value, got %v", tc.field, tc.lit, arg.Value)
			assert.InDelta(t, tc.want, arg.GetDoubleValue(), math.Abs(tc.want)*1e-15)
		}
	})

	t.Run("integer carrier is unchanged", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			field, lit string
			want       int64
		}{
			// An integer target recovers the value from its own type, so
			// the two's-complement encoding stays and is not ambiguous.
			{"uint64", "1e19", -8446744073709551616},
			{"uint64", maxUint64, -1},
			{"uint64", overInt64, -9223372036854775808},
			{"int64", maxInt64, 9223372036854775807},
			{"int32", "42", 42},
		} {
			arg := fieldAnnotationArg(t, tc.field, tc.lit)
			require.IsType(t, (*pwsv1.AnnotationArg_IntValue)(nil), arg.Value,
				"%s field, literal %s: want int_value, got %v", tc.field, tc.lit, arg.Value)
			assert.Equal(t, tc.want, arg.GetIntValue())
		}
	})

	t.Run("out of uint64 range converts only on a float carrier", func(t *testing.T) {
		t.Parallel()
		// #165's rule was carrier-independent: 1e100 lowered to
		// double_value everywhere, including onto integer fields that
		// cannot hold it. Under #188 the target decides — a float carrier
		// holds it, an integer carrier is a compile error — so only the
		// float half is asserted here; ir pins the rejections.
		for _, field := range []string{"double"} {
			arg := fieldAnnotationArg(t, field, "1e100")
			require.IsType(t, (*pwsv1.AnnotationArg_DoubleValue)(nil), arg.Value,
				"%s field: want double_value, got %v", field, arg.Value)
			assert.InDelta(t, 1e100, arg.GetDoubleValue(), 1e85)
		}
	})
}

// TestAnnotationUntypedArgWithoutACarrierKeepsSpelling pins the other
// side: an annotation on a message has no element type to convert to, so
// the literal's own type stands — which is what RFC-001 means by an `any`
// argument carrying its own typing.
//
// This test used to assert the opposite, and was the last live instance of
// #172's original symptom: `1e19` is written in floating-point notation
// but the lexer does not count a positive exponent as a float (#191), so
// it took the integer path and overflowed int64 to
// -8446744073709551616. Carrier routing could not reach it, because there
// is no carrier here — which is what showed the fix had been applied one
// layer too high (#188).
func TestAnnotationUntypedArgWithoutACarrierKeepsSpelling(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		lit  string
		want float64
	}{
		{"1e19", 1e19},
		{"1.5", 1.5},
		{"1e10", 1e10},
	} {
		f := compileForFDPTest(t, `syntax = "proto3";
package test;

annotation default(value: any);

@default(`+tc.lit+`)
message M {}
`)
		list, _ := proto.GetExtension(
			f.GetMessageType()[0].Options, pwsv1.E_MessageAnnotations).(*pwsv1.AnnotationList)
		require.NotNil(t, list, "carries no message annotations extension")
		require.Len(t, list.Entries, 1)
		require.Len(t, list.Entries[0].Args, 1)

		arg := list.Entries[0].Args[0]
		require.IsType(t, (*pwsv1.AnnotationArg_DoubleValue)(nil), arg.Value,
			"%s is written as a float and has no target; got %v", tc.lit, arg.Value)
		assert.InDelta(t, tc.want, arg.GetDoubleValue(), 0)
	}

	// An integer spelling still keeps its own type too.
	f := compileForFDPTest(t, `syntax = "proto3";
package test;

annotation default(value: any);

@default(42)
message M {}
`)
	list, _ := proto.GetExtension(
		f.GetMessageType()[0].Options, pwsv1.E_MessageAnnotations).(*pwsv1.AnnotationList)
	require.NotNil(t, list, "carries no message annotations extension")
	arg := list.Entries[0].Args[0]
	require.IsType(t, (*pwsv1.AnnotationArg_IntValue)(nil), arg.Value)
	assert.Equal(t, int64(42), arg.GetIntValue())
}

// wrapperAnnotationArg is [fieldAnnotationArg] for a field whose type is a
// google.protobuf wrapper, which needs the import.
func wrapperAnnotationArg(t *testing.T, wrapper, lit string) *pwsv1.AnnotationArg {
	t.Helper()
	return fieldArg(t, compileForFDPTest(t, fmt.Sprintf(`syntax = "proto3";
package test;

import "google/protobuf/wrappers.proto";

annotation default(value: any);

message M {
  google.protobuf.%s f = 1 @default(%s);
}
`, wrapper, lit)))
}

// TestAnnotationWrapperCarrierMatchesItsScalar pins #174 as the property it
// actually is: a google.protobuf wrapper carrier lowers exactly as the
// scalar it wraps.
//
// The wrappers are messages, so Predeclared reports nothing for them and
// #173's carrier routing skipped them — leaving `@default(1e19)` on a
// `DoubleValue` field at `int_value: -8446744073709551616`, the same
// (MaxInt64, MaxUint64] ambiguity #172 removed from bare `double`.
//
// Asserting equivalence rather than enumerating expected values means the
// two cannot drift apart: whatever the scalar rule becomes, the wrapper
// follows it.
func TestAnnotationWrapperCarrierMatchesItsScalar(t *testing.T) {
	t.Parallel()

	// The bound is exercised from both sides, as with #172: the whole
	// defect lives in one bit of range.
	// Each literal has to be one the carrier can hold: an out-of-range one
	// is now diagnosed (#177), and equality of rejection is asserted
	// separately below.
	for _, pair := range []struct {
		wrapper, scalar string
		literals        []string
	}{
		{"DoubleValue", "double", []string{"42", "1.5", "1e19", "18446744073709551616"}},
		{"FloatValue", "float", []string{"42", "1.5", "1e19"}},
		{"Int64Value", "int64", []string{"42", "9223372036854775807", "-9223372036854775808"}},
		{"UInt64Value", "uint64", []string{"42", "1e19", "18446744073709551615"}},
		{"Int32Value", "int32", []string{"42", "2147483647", "-2147483648"}},
		{"UInt32Value", "uint32", []string{"42", "4294967295"}},
	} {
		t.Run(pair.wrapper, func(t *testing.T) {
			t.Parallel()
			for _, lit := range pair.literals {
				want := fieldAnnotationArg(t, pair.scalar, lit)
				got := wrapperAnnotationArg(t, pair.wrapper, lit)
				assert.Equal(t, fmt.Sprintf("%T", want.Value), fmt.Sprintf("%T", got.Value),
					"%s and %s must pick the same oneof member for %s",
					pair.wrapper, pair.scalar, lit)
				assert.True(t, proto.Equal(want, got),
					"%s and %s must lower %s identically: %v vs %v",
					pair.wrapper, pair.scalar, lit, want.Value, got.Value)
			}
		})
	}
}

// TestAnnotationMapCarrierMatchesItsValueType pins #185 on the fdp side.
//
// A map field's element type is the synthesized `*Entry` message, so before
// #183's second decision a map carrier reported no scalar and fell back to
// the literal's own spelling — `map<string, bytes>` missed the bytes route
// #179 added and produced a descriptor that would not marshal, while
// `repeated bytes` was fine. The `ir` side pins the bound
// (TestCarrierBoundRoutesMapByValueType); this pins the lowering, in the
// shape the wrapper equivalence above uses: a map must agree with its own
// value type, member and value both.
func TestAnnotationMapCarrierMatchesItsValueType(t *testing.T) {
	t.Parallel()

	for _, pair := range []struct {
		value    string
		literals []string
	}{
		// A lone 0xff is not valid UTF-8, so bytes is the cell that
		// decides whether the descriptor can be marshalled at all.
		{"bytes", []string{`"\xff\xfe"`, `"ok"`}},
		{"double", []string{"42", "1.5", "1e19"}},
		{"uint64", []string{"42", "18446744073709551615"}},
		{"int32", []string{"42", "-2147483648"}},
		{"string", []string{`"hello"`}},
		{"bool", []string{"true"}},
	} {
		t.Run(pair.value, func(t *testing.T) {
			t.Parallel()
			for _, lit := range pair.literals {
				want := fieldAnnotationArg(t, pair.value, lit)
				got := fieldAnnotationArg(t, "map<string, "+pair.value+">", lit)
				assert.Equal(t, fmt.Sprintf("%T", want.Value), fmt.Sprintf("%T", got.Value),
					"map<string, %s> and %s must pick the same oneof member for %s",
					pair.value, pair.value, lit)
				assert.True(t, proto.Equal(want, got),
					"map<string, %s> and %s must lower %s identically: %v vs %v",
					pair.value, pair.value, lit, want.Value, got.Value)
			}
		})
	}
}

// TestAnnotationMapBytesCarrierMarshals states #185's headline symptom
// outright: the unmarshallable descriptor, one carrier over from #179's.
func TestAnnotationMapBytesCarrierMarshals(t *testing.T) {
	t.Parallel()

	f := compileForFDPTest(t, `syntax = "proto3";
package test;

annotation default(value: any);

message M {
  map<string, bytes> f = 1 @default("\xff\xfe");
}
`)
	arg := fieldArg(t, f)
	require.IsType(t, (*pwsv1.AnnotationArg_BytesValue)(nil), arg.Value,
		"a map with a bytes value must not route through string_value")
	assert.Equal(t, []byte{0xff, 0xfe}, arg.GetBytesValue())

	_, err := proto.Marshal(f)
	require.NoError(t, err, "the descriptor must serialize; this is the symptom #185 is about")
}

// TestAnnotationWrapperCarrierFixesTheBand states #174's headline outright,
// so the reported symptom is visible in the test name and not only implied
// by the equivalence above.
func TestAnnotationWrapperCarrierFixesTheBand(t *testing.T) {
	t.Parallel()

	arg := wrapperAnnotationArg(t, "DoubleValue", "1e19")
	require.IsType(t, (*pwsv1.AnnotationArg_DoubleValue)(nil), arg.Value,
		"got %v — a DoubleValue field must not receive the two's-complement int", arg.Value)
	assert.InDelta(t, 1e19, arg.GetDoubleValue(), 1)
}

// TestAnnotationBytesCarrierTakesBytesValue is #179.
//
// `string_value` is a proto3 string and so must be valid UTF-8. A bytes
// default frequently is not, and the untyped path sent every string literal
// there regardless of the carrier — so `@default("\xff\xfe")` on a `bytes`
// field put raw ff fe into a string field and the descriptor could no
// longer be MARSHALLED. That breaks anything writing the image out, not
// only annotation-aware readers, which is why the assertion below is on
// proto.Marshal rather than on the member alone.
func TestAnnotationBytesCarrierTakesBytesValue(t *testing.T) {
	t.Parallel()

	// A lone 0xff is not valid UTF-8 in any position.
	const nonUTF8 = `"\xff\xfe"`

	t.Run("bytes carrier", func(t *testing.T) {
		t.Parallel()
		f := compileForFDPTest(t, `syntax = "proto3";
package test;

annotation default(value: any);

message M {
  bytes f = 1 @default(`+nonUTF8+`);
}
`)
		arg := fieldArg(t, f)
		require.IsType(t, (*pwsv1.AnnotationArg_BytesValue)(nil), arg.Value,
			"a bytes carrier must not route through string_value")
		assert.Equal(t, []byte{0xff, 0xfe}, arg.GetBytesValue())

		_, err := proto.Marshal(f)
		require.NoError(t, err, "the descriptor must serialize; this is the symptom #179 is about")
	})

	t.Run("BytesValue wrapper carrier", func(t *testing.T) {
		t.Parallel()
		f := compileForFDPTest(t, `syntax = "proto3";
package test;

import "google/protobuf/wrappers.proto";

annotation default(value: any);

message M {
  google.protobuf.BytesValue f = 1 @default(`+nonUTF8+`);
}
`)
		arg := fieldArg(t, f)
		require.IsType(t, (*pwsv1.AnnotationArg_BytesValue)(nil), arg.Value)
		assert.Equal(t, []byte{0xff, 0xfe}, arg.GetBytesValue())
		_, err := proto.Marshal(f)
		require.NoError(t, err)
	})

	// A string carrier is unchanged: string_value is right for it, and
	// routing it to bytes would be the mirror-image defect.
	for _, carrier := range []string{"string", "google.protobuf.StringValue"} {
		t.Run(carrier+" carrier is unchanged", func(t *testing.T) {
			t.Parallel()
			imp := ""
			if strings.HasPrefix(carrier, "google.protobuf.") {
				imp = "import \"google/protobuf/wrappers.proto\";\n"
			}
			f := compileForFDPTest(t, `syntax = "proto3";
package test;
`+imp+`
annotation default(value: any);

message M {
  `+carrier+` f = 1 @default("hello");
}
`)
			arg := fieldArg(t, f)
			require.IsType(t, (*pwsv1.AnnotationArg_StringValue)(nil), arg.Value)
			assert.Equal(t, "hello", arg.GetStringValue())
		})
	}

	// A DECLARED bytes parameter was already correct and must stay so —
	// it is the route the carrier path now reuses.
	t.Run("declared bytes parameter is unchanged", func(t *testing.T) {
		t.Parallel()
		f := compileForFDPTest(t, `syntax = "proto3";
package test;

annotation raw(value: bytes);

message M {
  bytes f = 1 @raw(`+nonUTF8+`);
}
`)
		arg := fieldArg(t, f)
		require.IsType(t, (*pwsv1.AnnotationArg_BytesValue)(nil), arg.Value)
		assert.Equal(t, []byte{0xff, 0xfe}, arg.GetBytesValue())
		_, err := proto.Marshal(f)
		require.NoError(t, err)
	})

	// A list argument reaches buildLiteralArg through buildListElement with
	// the SAME carrier, so the route has to hold element by element. The
	// scalar shape is not evidence for the list one: #178 landed the
	// carrier bound on the argument and had to be fixed in review to
	// descend into list elements, and nesting is the shape after that.
	t.Run("list elements follow the bytes carrier", func(t *testing.T) {
		t.Parallel()
		f := compileForFDPTest(t, `syntax = "proto3";
package test;

annotation default(value: any);

message M {
  bytes f = 1 @default([`+nonUTF8+`]);
}
`)
		elems := fieldArg(t, f).GetLiteral().GetList().GetElements()
		require.Len(t, elems, 1)
		require.IsType(t, (*pwsv1.LiteralValue_BytesValue)(nil), elems[0].Kind,
			"a list element on a bytes carrier must not route through string_value")
		assert.Equal(t, []byte{0xff, 0xfe}, elems[0].GetBytesValue())

		_, err := proto.Marshal(f)
		require.NoError(t, err)
	})

	// A list of lists is the shape after that: buildListElement lowers a
	// nested list back through buildArgValue, so the carrier has to survive
	// the second descent as well.
	t.Run("nested list elements follow the bytes carrier", func(t *testing.T) {
		t.Parallel()
		f := compileForFDPTest(t, `syntax = "proto3";
package test;

annotation default(value: any);

message M {
  bytes f = 1 @default([[`+nonUTF8+`]]);
}
`)
		outer := fieldArg(t, f).GetLiteral().GetList().GetElements()
		require.Len(t, outer, 1)
		inner := outer[0].GetLiteral().GetList().GetElements()
		require.Len(t, inner, 1)
		require.IsType(t, (*pwsv1.LiteralValue_BytesValue)(nil), inner[0].Kind)
		assert.Equal(t, []byte{0xff, 0xfe}, inner[0].GetBytesValue())

		_, err := proto.Marshal(f)
		require.NoError(t, err)
	})
}

// failNowSentinel is what recorderT.FailNow panics with, so a require
// failure unwinds the helper the way testify's runtime.Goexit would
// instead of letting it run on into the nil dereference under test.
type failNowSentinel struct{}

// recorderT stands in for *testing.T and records failures instead of
// aborting the test, so a guard's failure can itself be asserted on.
type recorderT struct{ msgs []string }

func (r *recorderT) Errorf(format string, args ...any) {
	r.msgs = append(r.msgs, fmt.Sprintf(format, args...))
}
func (r *recorderT) FailNow() { panic(failNowSentinel{}) }
func (r *recorderT) Helper()  {}

// run reports whatever fn panicked with, treating a FailNow as a clean
// return. A non-nil result is a real panic — the failure mode #187 is
// about.
func (r *recorderT) run(fn func()) (panicked any) {
	defer func() {
		if v := recover(); v != nil {
			if _, ok := v.(failNowSentinel); !ok {
				panicked = v
			}
		}
	}()
	fn()
	return nil
}

// entriesSink defeats any elision of the nil dereference below.
var entriesSink []*pwsv1.Annotation

// TestExtensionGuardReportsRatherThanPanics is #187's done-when: a
// fixture whose carrier emits no extension at all, showing the guard
// reporting a clean failure where the assertion it replaced could not
// fail and left the next line to panic.
//
// No other test in this package reaches this state — every fixture emits
// an annotation — which is why the 48 assertions swept here had no check
// exercising them.
func TestExtensionGuardReportsRatherThanPanics(t *testing.T) {
	t.Parallel()

	// Options present (so the earlier options guard is not what fires),
	// but no annotation extension on them.
	f := compileForFDPTest(t, `syntax = "proto3";
package test;

message M {
  int32 f = 1 [deprecated = true];
}
`)
	field := f.GetMessageType()[0].GetField()[0]
	require.NotNil(t, field.GetOptions(), "fixture must carry options")

	t.Run("the replaced assertion could not have failed", func(t *testing.T) {
		t.Parallel()
		list, ok := proto.GetExtension(
			field.GetOptions(), pwsv1.E_FieldAnnotations).(*pwsv1.AnnotationList)
		assert.True(t, ok,
			"the type assertion succeeds on an unset extension, which is why "+
				"asserting on it has no teeth")
		assert.Nil(t, list, "and yields a typed nil")
		assert.Panics(t, func() { entriesSink = list.Entries },
			"the field access after it panics — the pre-#187 failure mode")
	})

	t.Run("the guard reports instead", func(t *testing.T) {
		t.Parallel()
		rec := new(recorderT)
		panicked := rec.run(func() { fieldArg(rec, f) })
		assert.Nil(t, panicked, "fieldArg must report the missing extension, not panic")
		require.NotEmpty(t, rec.msgs, "fieldArg must record a failure")
		assert.Contains(t, strings.Join(rec.msgs, "\n"), "field carries no AnnotationList extension",
			"the failure must name what was missing")
	})
}

// compileForFDPTestDiags compiles src and returns the descriptor together
// with every diagnostic at Error level or worse. Unlike compileForFDPTest
// it does not fail on diagnostics, so a test can assert that source IS
// rejected — and how many times, which is what catches a carrier being
// visited twice.
func compileForFDPTestDiags(t *testing.T, src string) (*descriptorpb.FileDescriptorProto, []string) {
	t.Helper()
	opener := source.NewMap(map[string]*source.File{
		"x.proto": source.NewFile("x.proto", src),
	})
	results, rep, err := incremental.Run(t.Context(), incremental.New(), queries.IR{
		Opener:  &source.Openers{opener, source.WKTs()},
		Session: new(ir.Session),
		Path:    "x.proto",
	})
	require.NoError(t, err)

	var errs []string
	for _, d := range rep.Diagnostics {
		if d.Level() <= report.Error {
			errs = append(errs, d.Message())
		}
	}
	if len(results) == 0 || results[0].Value == nil {
		return nil, errs
	}
	out, err := fdp.DescriptorProto(results[0].Value)
	require.NoError(t, err)
	return out, errs
}

// TestNonUTF8IsDiagnosedExactlyWhereItReachesStringValue is #184.
//
// #179 fixed a non-UTF-8 literal on a `bytes` carrier by routing it to
// bytes_value. Every other carrier still reached string_value, which is a
// protobuf `string` and cannot hold those bytes, so the descriptor still
// failed to marshal — the same symptom, one carrier over.
//
// The remedy is to diagnose rather than to re-route on content: see
// checkStringLiteralUTF8 in ir for why. This pins the rule from both
// sides, and pins it against the lowering: a case that is NOT diagnosed
// must marshal.
func TestNonUTF8IsDiagnosedExactlyWhereItReachesStringValue(t *testing.T) {
	t.Parallel()

	// A lone 0xff is not valid UTF-8 in any position.
	const bad = `"\xff\xfe"`

	t.Run("rejected where it reaches string_value", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct{ name, src string }{
			{"non-bytes scalar carrier", `annotation d(value: any);
message M { int32 f = 1 @d(` + bad + `); }`},
			{"string carrier", `annotation d(value: any);
message M { string f = 1 @d(` + bad + `); }`},
			{"no carrier at all", `annotation d(value: any);
@d(` + bad + `)
message M {}`},
			{"declared string parameter", `annotation d(value: string);
message M { int32 f = 1 @d(` + bad + `); }`},
			{"list element", `annotation d(value: any);
message M { int32 f = 1 @d([` + bad + `]); }`},
			{"map with a non-bytes value type", `annotation d(value: any);
message M { map<string, int32> f = 1 @d(` + bad + `); }`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				_, errs := compileForFDPTestDiags(t, `syntax = "proto3";
package test;

`+tc.src+`
`)
				assert.NotEmpty(t, errs,
					"a non-UTF-8 literal reaching string_value must be diagnosed")
			})
		}
	})

	t.Run("accepted where it reaches bytes_value, and marshals", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct{ name, src string }{
			{"bytes carrier", `annotation d(value: any);
message M { bytes f = 1 @d(` + bad + `); }`},
			{"declared bytes parameter", `annotation d(value: bytes);
message M { int32 f = 1 @d(` + bad + `); }`},
			{"BytesValue wrapper carrier", `import "google/protobuf/wrappers.proto";
annotation d(value: any);
message M { google.protobuf.BytesValue f = 1 @d(` + bad + `); }`},
			{"map with a bytes value type", `annotation d(value: any);
message M { map<string, bytes> f = 1 @d(` + bad + `); }`},
			{"repeated bytes carrier", `annotation d(value: any);
message M { repeated bytes f = 1 @d(` + bad + `); }`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				f, errs := compileForFDPTestDiags(t, `syntax = "proto3";
package test;

`+tc.src+`
`)
				assert.Empty(t, errs,
					"a bytes target carries arbitrary content and must not be diagnosed")
				require.NotNil(t, f)
				_, err := proto.Marshal(f)
				require.NoError(t, err,
					"anything that compiles clean must serialize; this is what #184 is about")
			})
		}
	})
}

// TestMapFieldAnnotationIsVisitedOnce pins the walk fix this change needed.
//
// A synthesized map entry reports the annotations of the field that
// produced it, so allAnnotationUses yielded every map-field annotation
// twice — once through the field, once through the entry message with the
// zero carrier. That was invisible while the only zero-carrier check
// returned immediately; checkStringLiteralUTF8 is the first that does not.
//
// The descriptor was never wrong — the duplicate lived only in the ir
// walk — so asserting on the descriptor would not catch this. Counting
// the diagnostics does: a doubly-visited argument is reported twice.
func TestMapFieldAnnotationIsVisitedOnce(t *testing.T) {
	t.Parallel()

	_, errs := compileForFDPTestDiags(t, `syntax = "proto3";
package test;

annotation d(value: any);

message M {
  map<string, int32> f = 1 @d("\xff\xfe");
}
`)
	assert.Len(t, errs, 1,
		"one bad argument, one diagnostic; two means the map entry was walked as a carrier too")
}

// TestAnyArgumentConvertsToItsTarget is #188's headline: one rule, not a
// routing hint plus a bound that mirrors it.
//
// An `any` argument is typed by its own literal and then converted to the
// type of the element it annotates. Every shape below reaches that rule
// through a different path — a bare scalar, a wrapper message, a map
// value, a repeated element, and no target at all — and one literal
// therefore has one answer everywhere.
func TestAnyArgumentConvertsToItsTarget(t *testing.T) {
	t.Parallel()

	t.Run("a float target holds 1e19 whatever shape it arrives in", func(t *testing.T) {
		t.Parallel()
		for _, field := range []string{
			"double",
			"google.protobuf.DoubleValue",
			"map<string, double>",
			"repeated double",
		} {
			arg := fieldAnnotationArgImporting(t, field, "1e19")
			require.IsType(t, (*pwsv1.AnnotationArg_DoubleValue)(nil), arg.Value,
				"%s: want double_value, got %v", field, arg.Value)
			assert.InDelta(t, 1e19, arg.GetDoubleValue(), 1)
		}
	})

	t.Run("no target leaves the literal its own type", func(t *testing.T) {
		t.Parallel()
		f := compileForFDPTest(t, `syntax = "proto3";
package test;

annotation default(value: any);

@default(1e19)
message M {}
`)
		list, _ := proto.GetExtension(
			f.GetMessageType()[0].Options, pwsv1.E_MessageAnnotations).(*pwsv1.AnnotationList)
		require.NotNil(t, list, "carries no message annotations extension")
		arg := list.Entries[0].Args[0]
		require.IsType(t, (*pwsv1.AnnotationArg_DoubleValue)(nil), arg.Value)
		assert.InDelta(t, 1e19, arg.GetDoubleValue(), 1)
	})

	t.Run("an integer target cannot hold 1e19", func(t *testing.T) {
		t.Parallel()
		for _, field := range []string{"int32", "int64"} {
			_, errs := compileForFDPTestDiags(t, `syntax = "proto3";
package test;

annotation default(value: any);

message M {
  `+field+` f = 1 @default(1e19);
}
`)
			require.NotEmpty(t, errs, "%s must reject 1e19", field)
			assert.Contains(t, errs[0], "out of range",
				"%s: the diagnostic must say what is wrong", field)
			assert.Contains(t, errs[0], field,
				"%s: the diagnostic must name the target type", field)
		}
	})

	t.Run("a float spelling that is an integer converts to the target", func(t *testing.T) {
		t.Parallel()
		// The member follows the TARGET, not the spelling: `1e2` is 100,
		// and an int32 field carries an integer.
		arg := fieldAnnotationArg(t, "int32", "1e2")
		require.IsType(t, (*pwsv1.AnnotationArg_IntValue)(nil), arg.Value,
			"got %v", arg.Value)
		assert.Equal(t, int64(100), arg.GetIntValue())
	})

	t.Run("a float spelling that is not an integer does not", func(t *testing.T) {
		t.Parallel()
		_, errs := compileForFDPTestDiags(t, `syntax = "proto3";
package test;

annotation default(value: any);

message M {
  int32 f = 1 @default(1.5);
}
`)
		require.NotEmpty(t, errs)
		assert.Contains(t, errs[0], "not an integer")
	})
}

// TestAnyArgumentKindMismatchIsDiagnosed covers the cell the family never
// had an answer for. Before #188 every one of these compiled with zero
// diagnostics, and one of them emitted a descriptor that could not be
// marshalled at all.
func TestAnyArgumentKindMismatchIsDiagnosed(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ field, lit string }{
		{"int32", `"hello"`},
		{"int32", `"\xff\xfe"`},
		{"int64", `"hello"`},
		{"uint32", `"hello"`},
		{"double", `"hello"`},
		{"float", `"3.14"`},
		{"bool", `"true"`},
		{"string", "42"},
		{"string", "true"},
		{"bool", "42"},
		{"int32", "true"},
		{"bytes", "42"},
	} {
		t.Run(tc.field+"_"+tc.lit, func(t *testing.T) {
			t.Parallel()
			_, errs := compileForFDPTestDiags(t, `syntax = "proto3";
package test;

annotation default(value: any);

message M {
  `+tc.field+` f = 1 @default(`+tc.lit+`);
}
`)
			require.NotEmpty(t, errs,
				"%s carrier must reject %s", tc.field, tc.lit)
			assert.Contains(t, errs[0], "cannot hold",
				"the diagnostic must name the literal's type and the target")
		})
	}
}

// argOnFieldOfMessage is [fieldArg] for a file that declares more than one
// message: a message-typed carrier needs a message to point at, so the
// "exactly one message" shape those helpers assume does not hold.
func argOnFieldOfMessage(
	t helperT, f *descriptorpb.FileDescriptorProto, name string,
) *pwsv1.AnnotationArg {
	t.Helper()
	for _, msg := range f.GetMessageType() {
		if msg.GetName() != name {
			continue
		}
		require.Len(t, msg.GetField(), 1)
		opts := msg.GetField()[0].GetOptions()
		require.NotNil(t, opts, "field carries no options")
		list, _ := proto.GetExtension(opts, pwsv1.E_FieldAnnotations).(*pwsv1.AnnotationList)
		require.NotNil(t, list, "field carries no AnnotationList extension")
		require.Len(t, list.GetEntries(), 1)
		require.Len(t, list.GetEntries()[0].GetArgs(), 1)
		return list.GetEntries()[0].GetArgs()[0]
	}
	require.FailNow(t, "no message named "+name)
	return nil
}

// TestAnyArgumentOnACarrierWithNoScalarKeepsItsOwnType is the arm the rest
// of this family never enters: a carrier whose CarrierScalar() is
// predeclared.Unknown.
//
// Every other carrier tested here — scalars, wrappers, maps, repeated
// scalars — resolves to a known scalar, so the Unknown arm was written,
// commented, and never run. It is not an exotic corner: a message-typed
// field, an enum-typed field and a non-wrapper well-known type are all
// ordinary schema, and Unknown is predeclared.Name's ZERO value, so this
// is the un-set state of the very thing the conversion rule keys on.
//
// There is nothing to convert to there, so the literal keeps its own type
// — which is what RFC-001 means by an `any` argument carrying its own
// typing, and what ConvertArgKind's doc states for a target of Unknown.
// checkCarrierRange substituted int64 before asking, so a string, a
// boolean or a fraction on one of these was rejected as unable to fit
// `int_value` while buildLiteralArg lowered it to string_value /
// bool_value / double_value: the two sides argTarget's comment says "must
// agree" disagreeing again, one layer down from where #188 fixed it.
//
// The sweep cannot see this. A wrongly-rejected cell does not compile, so
// it is skipped rather than failed — "compiles and marshals" is blind to
// anything that stops compiling. This asserts the member directly.
func TestAnyArgumentOnACarrierWithNoScalarKeepsItsOwnType(t *testing.T) {
	t.Parallel()

	for _, carrier := range []string{
		"Inner", "E", "google.protobuf.Timestamp",
		"repeated Inner", "map<string, Inner>",
	} {
		for _, tc := range []struct {
			lit  string
			want any
		}{
			{`"hello"`, (*pwsv1.AnnotationArg_StringValue)(nil)},
			{"true", (*pwsv1.AnnotationArg_BoolValue)(nil)},
			{"false", (*pwsv1.AnnotationArg_BoolValue)(nil)},
			{"1.5", (*pwsv1.AnnotationArg_DoubleValue)(nil)},
			// Float-spelled: #191's spelling rule reaches here too, and
			// double_value holds 1e19 exactly, so nothing wraps.
			{"1e19", (*pwsv1.AnnotationArg_DoubleValue)(nil)},
			{"42", (*pwsv1.AnnotationArg_IntValue)(nil)},
		} {
			t.Run(carrier+"_"+tc.lit, func(t *testing.T) {
				t.Parallel()
				f, errs := compileForFDPTestDiags(t, `syntax = "proto3";
package test;

import "google/protobuf/timestamp.proto";

annotation default(value: any);

enum E { E_ZERO = 0; }
message Inner { string s = 1; }

message M {
  `+carrier+` f = 1 @default(`+tc.lit+`);
}
`)
				require.Empty(t, errs,
					"%s has no scalar reading, so %s keeps its own type", carrier, tc.lit)
				require.IsType(t, tc.want, argOnFieldOfMessage(t, f, "M").Value)
			})
		}
	}

	// An integer past int64 is the one thing still rejected here, and for
	// a reason that survives: it DOES land in int_value, where it wraps to
	// a negative (#176). The bound is about the member, not the magnitude.
	t.Run("a band literal that still reaches int_value is rejected", func(t *testing.T) {
		t.Parallel()
		_, errs := compileForFDPTestDiags(t, `syntax = "proto3";
package test;

annotation default(value: any);

message Inner { string s = 1; }

message M {
  Inner f = 1 @default(10000000000000000000);
}
`)
		require.NotEmpty(t, errs)
		assert.Contains(t, errs[0], "out of range")
	})
}

// TestNonValueAnnotationMustDeclareItsParameter records the cost Option A
// accepts deliberately, so it is visible rather than discovered.
//
// `@since` carries a timestamp, not a value for the field. Left `any`, it
// is converted to the annotated element's type and rejected. The remedy is
// to declare what the parameter carries, which the diagnostic says.
func TestNonValueAnnotationMustDeclareItsParameter(t *testing.T) {
	t.Parallel()

	_, errs := compileForFDPTestDiags(t, `syntax = "proto3";
package test;

annotation since(value: any);

message M {
  int32 f = 1 @since(20250101000000);
}
`)
	require.NotEmpty(t, errs,
		"an untyped argument is converted to the annotated element's type")

	// Declaring the parameter is the fix, and it compiles.
	_, errs = compileForFDPTestDiags(t, `syntax = "proto3";
package test;

annotation since(value: int64);

message M {
  int32 f = 1 @since(20250101000000);
}
`)
	assert.Empty(t, errs, "a declared parameter converts against its declaration, not the carrier")
}

// fieldAnnotationArgImporting is [fieldAnnotationArg] for field types that
// may need the wrappers import.
func fieldAnnotationArgImporting(t *testing.T, fieldType, lit string) *pwsv1.AnnotationArg {
	t.Helper()
	imports := ""
	if strings.Contains(fieldType, "google.protobuf.") {
		imports = "import \"google/protobuf/wrappers.proto\";\n"
	}
	return fieldArg(t, compileForFDPTest(t, fmt.Sprintf(`syntax = "proto3";
package test;

%s
annotation default(value: any);

message M {
  %s f = 1 @default(%s);
}
`, imports, fieldType, lit)))
}

// TestEverythingThatCompilesMarshals is the invariant underneath this
// whole family, swept rather than sampled: whatever a file means, a
// descriptor this compiler emits without diagnostics must serialize.
//
// #179 and #184 were each one cell where it did not. The sweep is what
// makes the next cell fail here rather than downstream.
func TestEverythingThatCompilesMarshals(t *testing.T) {
	t.Parallel()

	carriers := []string{
		"double", "float", "int32", "int64", "uint32", "uint64",
		"sint32", "sint64", "fixed32", "fixed64", "sfixed32", "sfixed64",
		"bool", "string", "bytes",
		"repeated int32", "repeated bytes", "repeated string",
		"map<string, int32>", "map<string, bytes>", "map<string, double>",
		// Carriers whose CarrierScalar() is predeclared.Unknown — where
		// there is no type to convert to and the literal keeps its own.
		// Every carrier above resolves to a known scalar, so without these
		// the sweep never entered that arm at all, and it is the arm the
		// kind check reads: a string on one of these was rejected as
		// unable to fit `int_value` while fdp lowered it to `string_value`.
		// A message, an enum, a non-wrapper WKT, and both containers of a
		// message, since a map's value type and a repeated element are
		// resolved separately.
		"Inner", "E", "google.protobuf.Timestamp",
		"repeated Inner", "map<string, Inner>",
	}
	literals := []string{
		"42", "-42", "0", "1.5", "-1.5", "1e2", "1e19", "1e100",
		"18446744073709551615", "-18446744073709551615",
		"10000000000000000000", "1e400",
		`"hello"`, `"\xff\xfe"`, `""`, "true", "false",
		"[1, 2]", `["a", "b"]`, "[]",
	}

	compiled, marshalled := 0, 0
	for _, carrier := range carriers {
		for _, lit := range literals {
			f, errs := compileForFDPTestDiags(t, `syntax = "proto3";
package test;

import "google/protobuf/timestamp.proto";

annotation default(value: any);

enum E { E_ZERO = 0; }
message Inner { string s = 1; }

message M {
  `+carrier+` f = 1 @default(`+lit+`);
}
`)
			if len(errs) > 0 || f == nil {
				continue
			}
			compiled++
			_, err := proto.Marshal(f)
			if assert.NoError(t, err,
				"%s @default(%s) compiled clean but does not serialize", carrier, lit) {
				marshalled++
			}
		}
	}

	// A positive marker: if the fixtures stopped compiling for an
	// unrelated reason this would silently assert nothing.
	require.Greater(t, compiled, 100,
		"the sweep must actually exercise cases; got %d of %d",
		compiled, len(carriers)*len(literals))
	assert.Equal(t, compiled, marshalled)
	t.Logf("sweep: %d of %d cells compiled, %d marshalled",
		compiled, len(carriers)*len(literals), marshalled)
}

// helperT is what the extraction helpers need from *testing.T: testify's
// assertion surface plus Helper(). Taking the interface rather than the
// concrete type lets TestExtensionGuardReportsRatherThanPanics drive
// fieldArg with a recorder and observe the failure a real *testing.T
// would abort on — the only way to prove the guards added for #187
// actually report.
type helperT interface {
	require.TestingT
	Helper()
}

// fieldArg returns the single annotation argument on the single field of
// the single message in f.
//
// The absent-extension state is asserted against the VALUE, not against a
// type assertion: proto.GetExtension hands back a typed nil for a
// message-typed extension that is not set, so `v, ok := …
// .(*pwsv1.AnnotationList)` succeeds with ok == true and a nil list. That
// assertion can never fail, and the field access after it panics instead
// of reporting — on precisely the state these tests exist to catch, a
// carrier that stopped emitting its AnnotationList.
func fieldArg(t helperT, f *descriptorpb.FileDescriptorProto) *pwsv1.AnnotationArg {
	t.Helper()
	require.Len(t, f.GetMessageType(), 1)
	require.Len(t, f.GetMessageType()[0].GetField(), 1)
	fdp := f.GetMessageType()[0].GetField()[0]
	require.NotNil(t, fdp.GetOptions(), "field carries no options")
	list, _ := proto.GetExtension(fdp.GetOptions(), pwsv1.E_FieldAnnotations).(*pwsv1.AnnotationList)
	require.NotNil(t, list, "field carries no AnnotationList extension")
	require.Len(t, list.GetEntries(), 1)
	require.Len(t, list.GetEntries()[0].GetArgs(), 1)
	return list.GetEntries()[0].GetArgs()[0]
}
