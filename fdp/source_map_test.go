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
	pwsv1 "github.com/trendvidia/protocompile/gen/protowire/schema/v1"
	"github.com/trendvidia/protocompile/incremental"
	"github.com/trendvidia/protocompile/incremental/queries"
	"github.com/trendvidia/protocompile/ir"
	"github.com/trendvidia/protocompile/source"
)

// TestSourceMapBasic verifies every resolved annotation use
// produces a SourceEntry keyed by the canonical descriptor_path
// (elementPath[annotationFQN#ordinal]), with the carrier-derived
// entry kind, and a source_location pointing back to the use's
// @-token within the source file.
func TestSourceMapBasic(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

annotation tag(name: string);

@tag("alpha")
message M {
  string s = 1 @tag("beta");
}
`
	f := compileForFDPTest(t, src)
	require.NotNil(t, f.Options)
	sm, ok := proto.GetExtension(f.Options, pwsv1.E_SourceMap).(*pwsv1.SourceMap)
	require.True(t, ok)
	require.NotNil(t, sm)
	assert.Equal(t, "x.proto", sm.GetFile())

	byPath := map[string]*pwsv1.SourceEntry{}
	for _, e := range sm.GetEntries() {
		byPath[e.GetDescriptorPath()] = e
	}
	require.Len(t, byPath, 2,
		"one entry per carrier (message M, field M.s) is expected")

	if e := byPath["test.M[test.tag#0]"]; assert.NotNil(t, e, "missing entry for test.M[test.tag#0]") {
		assert.Equal(t, pwsv1.EntryKind_MESSAGE_VALIDATE, e.GetKind())
		require.NotNil(t, e.GetSourceLocation())
		assert.Equal(t, "x.proto", e.GetSourceLocation().GetFile())
		assert.Positive(t, e.GetSourceLocation().GetLine())
		assert.Positive(t, e.GetSourceLocation().GetColumn())
	}
	if e := byPath["test.M.s[test.tag#0]"]; assert.NotNil(t, e, "missing entry for test.M.s[test.tag#0]") {
		assert.Equal(t, pwsv1.EntryKind_FIELD_VALIDATE, e.GetKind())
		assert.Equal(t, "x.proto", e.GetSourceLocation().GetFile())
	}
}

// TestSourceMapMultipleUsesOnSameCarrier verifies anchor ordinals:
// same-named annotations on one carrier count zero-based in list
// order, and differently-named ones each start at #0.
func TestSourceMapMultipleUsesOnSameCarrier(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

annotation a(n: int32);
annotation b;

@a(1)
@b
@a(2)
message M {}
`
	f := compileForFDPTest(t, src)
	sm, ok := proto.GetExtension(f.Options, pwsv1.E_SourceMap).(*pwsv1.SourceMap)
	require.True(t, ok)
	require.NotNil(t, sm)

	paths := make([]string, 0, len(sm.GetEntries()))
	for _, e := range sm.GetEntries() {
		paths = append(paths, e.GetDescriptorPath())
	}
	assert.ElementsMatch(t, []string{
		"test.M[test.a#0]",
		"test.M[test.b#0]",
		"test.M[test.a#1]",
	}, paths)
}

// TestSourceMapNoEntriesWithoutAnnotations verifies the extension
// is *not* attached when the file has no annotation uses, even if
// FileOptions exists for other reasons. Downstream tools rely on a
// missing SourceMap to mean "no PSE entries" rather than "empty".
func TestSourceMapNoEntriesWithoutAnnotations(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

message M {
  string s = 1;
}
`
	f := compileForFDPTest(t, src)
	// FileOptions may be nil entirely here, which already means no
	// SourceMap. If it exists, the extension must not be set.
	if f.Options == nil {
		return
	}
	sm, has := proto.GetExtension(f.Options, pwsv1.E_SourceMap).(*pwsv1.SourceMap)
	if !has || sm == nil {
		return
	}
	assert.Empty(t, sm.GetEntries(),
		"no annotation uses means no SourceEntries")
}

// TestSourceMapTypeRefinementSingleAlias verifies that a field
// declared with a single-link type alias emits one TYPE_REFINEMENT
// entry whose type_chain has the alias as its only link. The
// entry's source_location points to the field's use site (where
// "Email" was written) and the link's declaration_location points
// to the `type Email = string` declaration.
func TestSourceMapTypeRefinementSingleAlias(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

type Email = string;

message User {
  Email email = 1;
}
`
	f := compileForFDPTest(t, src)
	require.NotNil(t, f.Options)
	sm, ok := proto.GetExtension(f.Options, pwsv1.E_SourceMap).(*pwsv1.SourceMap)
	require.True(t, ok)
	require.NotNil(t, sm)

	var refinements []*pwsv1.SourceEntry
	for _, e := range sm.GetEntries() {
		if e.GetKind() == pwsv1.EntryKind_TYPE_REFINEMENT {
			refinements = append(refinements, e)
		}
	}
	require.Len(t, refinements, 1, "exactly one TYPE_REFINEMENT entry")
	r := refinements[0]
	assert.Equal(t, "test.User.email", r.GetDescriptorPath())
	require.NotNil(t, r.GetSourceLocation())
	assert.Equal(t, "x.proto", r.GetSourceLocation().GetFile())
	assert.Positive(t, r.GetSourceLocation().GetLine())

	require.Len(t, r.GetTypeChain(), 1)
	link := r.GetTypeChain()[0]
	assert.Equal(t, "test.Email", link.GetTypeFqn())
	require.NotNil(t, link.GetDeclarationLocation())
	assert.Equal(t, "x.proto", link.GetDeclarationLocation().GetFile())
	assert.Positive(t, link.GetDeclarationLocation().GetLine())
}

// TestSourceMapTypeRefinementChain verifies a multi-link alias
// chain produces one TYPE_REFINEMENT entry whose type_chain has
// one link per alias, in base-to-derived order (the alias the
// field was declared with last).
func TestSourceMapTypeRefinementChain(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

type B = string;
type A = test.B;

message M {
  A x = 1;
}
`
	f := compileForFDPTest(t, src)
	sm, ok := proto.GetExtension(f.Options, pwsv1.E_SourceMap).(*pwsv1.SourceMap)
	require.True(t, ok)
	require.NotNil(t, sm)

	var refinement *pwsv1.SourceEntry
	for _, e := range sm.GetEntries() {
		if e.GetKind() == pwsv1.EntryKind_TYPE_REFINEMENT {
			refinement = e
			break
		}
	}
	require.NotNil(t, refinement)
	require.Len(t, refinement.GetTypeChain(), 2,
		"both A and B should appear as separate links")
	assert.Equal(t, "test.B", refinement.GetTypeChain()[0].GetTypeFqn(),
		"base-most alias (B) appears first")
	assert.Equal(t, "test.A", refinement.GetTypeChain()[1].GetTypeFqn(),
		"the alias the field was declared with (A) follows")
}

// TestSourceMapTypeRefinementMapValue verifies that a map field
// whose value type is an alias gets the field's own bare-path
// TYPE_REFINEMENT entry (issue #109): the chain is the value type's,
// and the source_location points at the value-type name inside
// `map<K, V>` — not at the whole map type, and not at a synthetic
// entry member.
func TestSourceMapTypeRefinementMapValue(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

type Email = string;

message Book {
  map<string, Email> contacts = 1;
}
`
	f := compileForFDPTest(t, src)
	sm, ok := proto.GetExtension(f.Options, pwsv1.E_SourceMap).(*pwsv1.SourceMap)
	require.True(t, ok)
	require.NotNil(t, sm)

	var refinements []*pwsv1.SourceEntry
	for _, e := range sm.GetEntries() {
		if e.GetKind() == pwsv1.EntryKind_TYPE_REFINEMENT {
			refinements = append(refinements, e)
		}
	}
	require.Len(t, refinements, 1,
		"one entry for the map field; none for synthetic entry members")
	r := refinements[0]
	assert.Equal(t, "test.Book.contacts", r.GetDescriptorPath())
	require.NotNil(t, r.GetSourceLocation())
	assert.Equal(t, int32(7), r.GetSourceLocation().GetLine())
	assert.Equal(t, int32(15), r.GetSourceLocation().GetColumn(),
		"points at `Email` inside map<string, Email>")

	require.Len(t, r.GetTypeChain(), 1)
	assert.Equal(t, "test.Email", r.GetTypeChain()[0].GetTypeFqn())
}

// TestSourceMapTypeRefinementNoEntryForConcreteType verifies that
// a field declared with a concrete (non-alias) type does not
// produce a TYPE_REFINEMENT entry. Only fields whose declared type
// actually went through an alias chain should emit one.
func TestSourceMapTypeRefinementNoEntryForConcreteType(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

annotation k;

message Inner {}

message M {
  string s = 1 @k;
  Inner inner = 2;
}
`
	f := compileForFDPTest(t, src)
	require.NotNil(t, f.Options)
	sm, ok := proto.GetExtension(f.Options, pwsv1.E_SourceMap).(*pwsv1.SourceMap)
	require.True(t, ok)
	require.NotNil(t, sm)

	for _, e := range sm.GetEntries() {
		assert.NotEqual(t, pwsv1.EntryKind_TYPE_REFINEMENT, e.GetKind(),
			"no TYPE_REFINEMENT should be emitted for concrete-typed fields")
	}
}

// TestSourceMapTypeRefinementCrossFileChain verifies a cross-file
// chain: the entry's source_location is in the consuming file
// (where the alias name was written), but each link's
// declaration_location points to wherever the alias was defined,
// which may be a different file.
func TestSourceMapTypeRefinementCrossFileChain(t *testing.T) {
	t.Parallel()

	const typesSrc = `syntax = "proto3";
package types;

type B = string;
`
	const userSrc = `syntax = "proto3";
package app;

import "types.proto";

type A = types.B;

message M {
  A x = 1;
}
`
	opener := source.NewMap(map[string]*source.File{
		"types.proto": source.NewFile("types.proto", typesSrc),
		"user.proto":  source.NewFile("user.proto", userSrc),
	})
	allOpeners := &source.Openers{opener, source.WKTs()}

	exec := incremental.New()
	sess := new(ir.Session)
	results, _, err := incremental.Run(t.Context(), exec, queries.IR{
		Opener:  allOpeners,
		Session: sess,
		Path:    "user.proto",
	})
	require.NoError(t, err)
	require.NotNil(t, results[0].Value)

	out, err := fdp.DescriptorProto(results[0].Value)
	require.NoError(t, err)
	require.NotNil(t, out.Options)
	sm, ok := proto.GetExtension(out.Options, pwsv1.E_SourceMap).(*pwsv1.SourceMap)
	require.True(t, ok)
	require.NotNil(t, sm)

	var refinement *pwsv1.SourceEntry
	for _, e := range sm.GetEntries() {
		if e.GetKind() == pwsv1.EntryKind_TYPE_REFINEMENT {
			refinement = e
			break
		}
	}
	require.NotNil(t, refinement)
	assert.Equal(t, "app.M.x", refinement.GetDescriptorPath())
	require.NotNil(t, refinement.GetSourceLocation())
	assert.Equal(t, "user.proto", refinement.GetSourceLocation().GetFile(),
		"the use site of the alias name lives in the consuming file")

	require.Len(t, refinement.GetTypeChain(), 2)
	assert.Equal(t, "types.B", refinement.GetTypeChain()[0].GetTypeFqn(),
		"base-most alias appears first")
	assert.Equal(t, "types.proto",
		refinement.GetTypeChain()[0].GetDeclarationLocation().GetFile(),
		"types.B is declared in types.proto")
	assert.Equal(t, "app.A", refinement.GetTypeChain()[1].GetTypeFqn())
	assert.Equal(t, "user.proto",
		refinement.GetTypeChain()[1].GetDeclarationLocation().GetFile(),
		"app.A is declared in user.proto")
}

// TestSourceMapCrossFileAliasPropagation verifies that an
// annotation that was propagated onto a field from a type alias
// living in another file produces a SourceEntry whose
// descriptor_path identifies the field (consumer side) but whose
// source_location.file points back to the alias's defining file.
// This is the unique value of SourceMap over SourceCodeInfo: it
// records the synthesized field-level use along with its true
// source position across files.
func TestSourceMapCrossFileAliasPropagation(t *testing.T) {
	t.Parallel()

	const typesSrc = `syntax = "proto3";
package types;

annotation validate(rule: string);

type Email = string @validate("size(this) > 0");
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
	results, _, err := incremental.Run(t.Context(), exec, queries.IR{
		Opener:  allOpeners,
		Session: sess,
		Path:    "user.proto",
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.NotNil(t, results[0].Value)

	out, err := fdp.DescriptorProto(results[0].Value)
	require.NoError(t, err)
	require.NotNil(t, out.Options)
	sm, ok := proto.GetExtension(out.Options, pwsv1.E_SourceMap).(*pwsv1.SourceMap)
	require.True(t, ok)
	require.NotNil(t, sm)
	assert.Equal(t, "user.proto", sm.GetFile(),
		"SourceMap.file is the emitting file, not the use's source file")

	var found *pwsv1.SourceEntry
	for _, e := range sm.GetEntries() {
		if e.GetDescriptorPath() == "app.User.email[types.validate#0]" {
			found = e
			break
		}
	}
	require.NotNil(t, found,
		"the propagated @validate should produce a SourceEntry for the field")
	assert.Equal(t, pwsv1.EntryKind_FIELD_VALIDATE, found.GetKind())
	require.NotNil(t, found.GetSourceLocation())
	assert.Equal(t, "types.proto", found.GetSourceLocation().GetFile(),
		"source location should point to the alias's defining file, not the consuming one")
	assert.Positive(t, found.GetSourceLocation().GetLine())
	assert.Positive(t, found.GetSourceLocation().GetColumn())
}

// TestSourceMapFunctionCalls verifies FUNCTION_CALL entries: each
// call site extracted from an expression argument gets an entry
// keyed elementPath[annotation#ordinal]/arg#i/call#j, where i is the
// lowered argument index and j the call's index in Expression.calls.
// Engine builtins produce no entries.
func TestSourceMapFunctionCalls(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

function in_region(value: string, regions: string);
function is_email(value: string);

annotation validate(rule: expression, code: string = "");

message Account {
  string country = 1
    @validate(in_region(this, ["US", "CA"]), code = "account.bad_region")
    @validate(is_email(this) || matches(this, "^x"), code = "account.bad_email");
}
`
	f := compileForFDPTest(t, src)
	require.NotNil(t, f.Options)
	sm, ok := proto.GetExtension(f.Options, pwsv1.E_SourceMap).(*pwsv1.SourceMap)
	require.True(t, ok)
	require.NotNil(t, sm)

	byPath := map[string]*pwsv1.SourceEntry{}
	for _, e := range sm.GetEntries() {
		byPath[e.GetDescriptorPath()] = e
	}

	// The two @validate anchors.
	require.Contains(t, byPath, "test.Account.country[test.validate#0]")
	require.Contains(t, byPath, "test.Account.country[test.validate#1]")
	assert.Equal(t, pwsv1.EntryKind_FIELD_VALIDATE,
		byPath["test.Account.country[test.validate#0]"].GetKind())

	// One call entry per resolved call site; matches() is a builtin
	// and produces none.
	call0 := byPath["test.Account.country[test.validate#0]/arg#0/call#0"]
	require.NotNil(t, call0, "in_region call entry missing; paths: %v", byPath)
	assert.Equal(t, pwsv1.EntryKind_FUNCTION_CALL, call0.GetKind())
	require.NotNil(t, call0.GetSourceLocation())
	assert.Equal(t, "x.proto", call0.GetSourceLocation().GetFile())
	assert.Positive(t, call0.GetSourceLocation().GetLine())

	call1 := byPath["test.Account.country[test.validate#1]/arg#0/call#0"]
	require.NotNil(t, call1, "is_email call entry missing")
	assert.Equal(t, pwsv1.EntryKind_FUNCTION_CALL, call1.GetKind())

	var callEntries int
	for path, e := range byPath {
		if e.GetKind() == pwsv1.EntryKind_FUNCTION_CALL {
			callEntries++
			// Every call path must parse under the canonical grammar.
			parsed, err := fdp.ParseDescriptorPath(path)
			require.NoError(t, err)
			assert.True(t, parsed.HasCall)
			assert.Equal(t, path, parsed.String())
		}
	}
	assert.Equal(t, 2, callEntries, "builtins must not produce FUNCTION_CALL entries")
}

// TestSourceMapEnumCarrierKinds verifies enums and enum values key
// their entries as ANNOTATION_USE (not FIELD/MESSAGE_VALIDATE), with
// enum values using their parent-scoped FullName per §8.3.1.
func TestSourceMapEnumCarrierKinds(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

annotation description(text: string);

@description("subscription tier")
enum Tier {
  TIER_UNSPECIFIED = 0 @description("not yet selected");
}
`
	f := compileForFDPTest(t, src)
	sm, ok := proto.GetExtension(f.Options, pwsv1.E_SourceMap).(*pwsv1.SourceMap)
	require.True(t, ok)
	require.NotNil(t, sm)

	byPath := map[string]*pwsv1.SourceEntry{}
	for _, e := range sm.GetEntries() {
		byPath[e.GetDescriptorPath()] = e
	}
	if e := byPath["test.Tier[test.description#0]"]; assert.NotNil(t, e) {
		assert.Equal(t, pwsv1.EntryKind_ANNOTATION_USE, e.GetKind())
	}
	if e := byPath["test.TIER_UNSPECIFIED[test.description#0]"]; assert.NotNil(t, e,
		"enum values key by their parent-scoped name; paths: %v", byPath) {
		assert.Equal(t, pwsv1.EntryKind_ANNOTATION_USE, e.GetKind())
	}
}

// TestSourceMapRoundTrip verifies the acceptance criterion:
// marshalling the descriptor, unmarshalling it, and re-marshalling
// preserves the source map byte-identically.
func TestSourceMapRoundTrip(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

function is_email(value: string);

annotation validate(rule: expression, code: string = "");
annotation description(text: string);

type Email = string @validate(is_email(this), code = "email.invalid");

@description("a registered user")
message User {
  Email email = 1 @description("primary contact");
}
`
	f := compileForFDPTest(t, src)
	require.NotNil(t, f.Options)

	marshal := func(m proto.Message) []byte {
		b, err := proto.MarshalOptions{Deterministic: true}.Marshal(m)
		require.NoError(t, err)
		return b
	}

	first := marshal(f)
	var reparsed descriptorpb.FileDescriptorProto
	require.NoError(t, proto.Unmarshal(first, &reparsed))
	second := marshal(&reparsed)
	require.Equal(t, first, second, "descriptor must re-marshal byte-identically")

	sm1, ok := proto.GetExtension(f.Options, pwsv1.E_SourceMap).(*pwsv1.SourceMap)
	require.True(t, ok)
	sm2, ok := proto.GetExtension(reparsed.Options, pwsv1.E_SourceMap).(*pwsv1.SourceMap)
	require.True(t, ok)
	require.Equal(t, marshal(sm1), marshal(sm2), "source map must survive the round trip byte-identically")
	require.NotEmpty(t, sm1.GetEntries())
}
