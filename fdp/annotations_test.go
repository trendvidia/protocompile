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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/trendvidia/protocompile/fdp"
	"github.com/trendvidia/protocompile/incremental"
	"github.com/trendvidia/protocompile/incremental/queries"
	pwsv1 "github.com/trendvidia/protocompile/internal/gen/protowire/schema/v1"
	"github.com/trendvidia/protocompile/ir"
	"github.com/trendvidia/protocompile/source"
)

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
	results, _, err := incremental.Run(t.Context(), exec, queries.IR{
		Opener:  allOpeners,
		Session: sess,
		Path:    "x.proto",
	})
	require.NoError(t, err)
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

// TestAnnotationEmissionPath verifies identifier-path args lower
// into Literal.enum_name.
func TestAnnotationEmissionPath(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

annotation scope(visibility: any);

@scope(myco.acme.public)
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
	assert.Equal(t, "myco.acme.public", lit.GetEnumName())
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

// TestAnnotationEmissionCarrierCoverage verifies the extension lands
// on the correct Options message for each carrier kind.
func TestAnnotationEmissionCarrierCoverage(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

annotation k;

@k
message M {
  @k
  string field_a = 1;
  @k
  oneof choice {
    string field_b = 2;
  }
}

@k
enum E {
  E_UNSET = 0;
  @k
  E_ONE = 1;
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
