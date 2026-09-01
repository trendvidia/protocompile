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

package collide_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trendvidia/protocompile/collide"
)

// TestCollisionIsDetected is the case issue #139 was filed about: two
// modules that each vendor a copy of the same .proto. Linking both panics
// in protoregistry.RegisterFile before main runs, and nothing catches it
// today because each module is individually clean.
func TestCollisionIsDetected(t *testing.T) {
	t.Parallel()

	got, err := collide.Check(t.Context(), []collide.Module{
		{Name: "chameleon", Root: "testdata/chameleon"},
		{Name: "voya", Root: "testdata/voya_vendored"},
	})
	require.NoError(t, err)
	require.NotEmpty(t, got, "the duplicated pxf/annotations.proto must be reported")

	// The colliding import path, naming both claimants.
	fileCollisions := byKind(got, collide.KindFile)
	require.Len(t, fileCollisions, 1)
	assert.Equal(t, "pxf/annotations.proto", fileCollisions[0].Name)
	assert.Equal(t, []collide.Claim{
		{Module: "chameleon", File: "pxf/annotations.proto"},
		{Module: "voya", File: "pxf/annotations.proto"},
	}, fileCollisions[0].Claims)

	// At least one colliding fully-qualified name, also naming both.
	symbols := byKind(got, collide.KindSymbol)
	require.NotEmpty(t, symbols)
	names := make([]string, 0, len(symbols))
	for _, c := range symbols {
		names = append(names, c.Name)
		assert.Equal(t, []collide.Claim{
			{Module: "chameleon", File: "pxf/annotations.proto"},
			{Module: "voya", File: "pxf/annotations.proto"},
		}, c.Claims, "symbol %s", c.Name)
	}
	assert.Contains(t, names, "pxf.required", "the extension is a top-level declaration")
	assert.Contains(t, names, "pxf.Constraint")
}

// TestNoCollisionWhenOnlyOneDefinesIt is the constraint the voya/chameleon
// comment currently states in prose: voya links chameleon and therefore
// must not vendor its own copy of pxf/annotations.proto. When it does not,
// the check passes.
func TestNoCollisionWhenOnlyOneDefinesIt(t *testing.T) {
	t.Parallel()

	got, err := collide.Check(t.Context(), []collide.Module{
		{Name: "chameleon", Root: "testdata/chameleon"},
		{Name: "voya", Root: "testdata/voya"},
	})
	require.NoError(t, err)
	assert.Empty(t, got)
}

// TestDependentModuleCompiles pins the configuration the check exists for
// and which its first fixtures did not have: voya *links* chameleon, so
// voya/service.proto imports pxf/annotations.proto, a file that is not
// under voya's own root. That import is the whole reason voya must not
// vendor its own copy.
//
// Compiling each module against only its own root fails to resolve it —
//
//	module voya: voya/service.proto:5:1: imported file does not exist
//
// — which reports the very arrangement being checked as unreadable. Every
// module's root is on the import path, and claims are still attributed
// only to the files a module actually contains.
func TestDependentModuleCompiles(t *testing.T) {
	t.Parallel()

	got, err := collide.Check(t.Context(), []collide.Module{
		{Name: "chameleon", Root: "testdata/chameleon"},
		{Name: "voya", Root: "testdata/voya"},
	})
	require.NoError(t, err, "voya imports chameleon's proto and must still compile")
	assert.Empty(t, got, "importing another module's file is not claiming it")
}

// TestVendoringWhileAlsoImportingCollides is the same link relationship
// with voya vendoring its own copy anyway. Its own root is searched first,
// so it resolves and therefore claims its copy — which is what makes the
// duplicate visible instead of silently unifying the two.
func TestVendoringWhileAlsoImportingCollides(t *testing.T) {
	t.Parallel()

	got, err := collide.Check(t.Context(), []collide.Module{
		{Name: "chameleon", Root: "testdata/chameleon"},
		{Name: "voya", Root: "testdata/voya_vendored"},
	})
	require.NoError(t, err)
	require.NotEmpty(t, got)
	assert.Equal(t, "pxf/annotations.proto", byKind(got, collide.KindFile)[0].Name)
}

// TestWellKnownTypesAreNotClaimed guards the obvious false positive: every
// module imports google/protobuf/descriptor.proto, and if imports counted
// as claims then any two modules would collide on it.
func TestWellKnownTypesAreNotClaimed(t *testing.T) {
	t.Parallel()

	got, err := collide.Check(t.Context(), []collide.Module{
		{Name: "a", Root: "testdata/chameleon"},
		{Name: "b", Root: "testdata/voya"},
	})
	require.NoError(t, err)
	assert.Empty(t, got, "an imported WKT is claimed by whoever ships it, not by its importers")
}

// TestSingleModuleNeverCollidesWithItself keeps the check from reporting a
// module against itself when the same root is listed once.
func TestSingleModuleNeverCollidesWithItself(t *testing.T) {
	t.Parallel()

	got, err := collide.Check(t.Context(), []collide.Module{
		{Name: "chameleon", Root: "testdata/chameleon"},
	})
	require.NoError(t, err)
	assert.Empty(t, got)
}

// TestBrokenModuleIsAnError pins that an unreadable module fails loudly.
// Reporting it as clean would be the same silent pass this package exists
// to remove.
func TestBrokenModuleIsAnError(t *testing.T) {
	t.Parallel()

	_, err := collide.Check(t.Context(), []collide.Module{
		{Name: "missing", Root: "testdata/does-not-exist"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing")
}

func TestValidation(t *testing.T) {
	t.Parallel()

	_, err := collide.Check(t.Context(), []collide.Module{
		{Name: "a", Root: "testdata/voya"},
		{Name: "a", Root: "testdata/chameleon"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate module name")

	_, err = collide.Check(t.Context(), []collide.Module{{Name: "", Root: "x"}})
	require.Error(t, err)

	_, err = collide.Check(t.Context(), []collide.Module{{Name: "a", Root: ""}})
	require.Error(t, err)
}

func byKind(cs []collide.Collision, k collide.Kind) []collide.Collision {
	var out []collide.Collision
	for _, c := range cs {
		if c.Kind == k {
			out = append(out, c)
		}
	}
	return out
}
