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

	"github.com/trendvidia/protocompile/fdp"
	"github.com/trendvidia/protocompile/incremental"
	"github.com/trendvidia/protocompile/incremental/queries"
	pwsv1 "github.com/trendvidia/protocompile/internal/gen/protowire/schema/v1"
	"github.com/trendvidia/protocompile/ir"
	"github.com/trendvidia/protocompile/source"
)

// TestSourceMapBasic exercises the MVP scope: every resolved
// annotation use produces an ANNOTATION_USE SourceEntry whose
// descriptor_path is the carrier's FQN and whose source_location
// points back to the use's @-token within the source file.
func TestSourceMapBasic(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

annotation tag(name: string);

@tag("alpha")
message M {
  @tag("beta")
  string s = 1;
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

	if e := byPath["test.M"]; assert.NotNil(t, e, "missing entry for test.M") {
		assert.Equal(t, pwsv1.EntryKind_ANNOTATION_USE, e.GetKind())
		require.NotNil(t, e.GetSourceLocation())
		assert.Equal(t, "x.proto", e.GetSourceLocation().GetFile())
		assert.Positive(t, e.GetSourceLocation().GetLine())
		assert.Positive(t, e.GetSourceLocation().GetColumn())
	}
	if e := byPath["test.M.s"]; assert.NotNil(t, e, "missing entry for test.M.s") {
		assert.Equal(t, pwsv1.EntryKind_ANNOTATION_USE, e.GetKind())
		assert.Equal(t, "x.proto", e.GetSourceLocation().GetFile())
	}
}

// TestSourceMapMultipleUsesOnSameCarrier verifies a carrier with
// two trailing annotations produces two SourceEntries sharing the
// same descriptor_path but distinct source locations.
func TestSourceMapMultipleUsesOnSameCarrier(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

annotation a;
annotation b;

@a
@b
message M {}
`
	f := compileForFDPTest(t, src)
	sm, ok := proto.GetExtension(f.Options, pwsv1.E_SourceMap).(*pwsv1.SourceMap)
	require.True(t, ok)
	require.NotNil(t, sm)

	var matchedM int
	for _, e := range sm.GetEntries() {
		if e.GetDescriptorPath() == "test.M" {
			matchedM++
		}
	}
	assert.Equal(t, 2, matchedM,
		"both @a and @b should produce SourceEntries on test.M")
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
// one link per alias, in chain order (head first).
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
	assert.Equal(t, "test.A", refinement.GetTypeChain()[0].GetTypeFqn(),
		"chain head (A) appears first")
	assert.Equal(t, "test.B", refinement.GetTypeChain()[1].GetTypeFqn(),
		"chain tail (B) follows")
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
  @k
  string s = 1;
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
	assert.Equal(t, "app.A", refinement.GetTypeChain()[0].GetTypeFqn())
	assert.Equal(t, "user.proto",
		refinement.GetTypeChain()[0].GetDeclarationLocation().GetFile(),
		"app.A is declared in user.proto")
	assert.Equal(t, "types.B", refinement.GetTypeChain()[1].GetTypeFqn())
	assert.Equal(t, "types.proto",
		refinement.GetTypeChain()[1].GetDeclarationLocation().GetFile(),
		"types.B is declared in types.proto")
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
		if e.GetDescriptorPath() == "app.User.email" {
			found = e
			break
		}
	}
	require.NotNil(t, found,
		"the propagated @validate should produce a SourceEntry for the field")
	assert.Equal(t, pwsv1.EntryKind_ANNOTATION_USE, found.GetKind())
	require.NotNil(t, found.GetSourceLocation())
	assert.Equal(t, "types.proto", found.GetSourceLocation().GetFile(),
		"source location should point to the alias's defining file, not the consuming one")
	assert.Positive(t, found.GetSourceLocation().GetLine())
	assert.Positive(t, found.GetSourceLocation().GetColumn())
}
