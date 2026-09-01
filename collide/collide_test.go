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
	"os"
	"path/filepath"
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

// TestTopLevelEnumValuesCollide covers the shape no fixture here had: an
// enum. An enum value is scoped to the enum's parent, not to the enum, so
// `protoregistry.RegisterFile` registers the values of a top-level enum at
// the top level alongside it (rangeTopLevelDescriptors, registry.go). Two
// differently-named enums in one Protobuf package that both spell a value
// `UNSPECIFIED` therefore conflict over px.UNSPECIFIED — registering both
// panics with `has a name conflict over px.UNSPECIFIED` — and listing only
// Messages/Enums/Extensions/Services reported that pair clean.
func TestTopLevelEnumValuesCollide(t *testing.T) {
	t.Parallel()

	got, err := collide.Check(t.Context(), []collide.Module{
		{Name: "alpha", Root: "testdata/enum_alpha"},
		{Name: "beta", Root: "testdata/enum_beta"},
	})
	require.NoError(t, err)

	names := make([]string, 0, len(got))
	for _, c := range got {
		names = append(names, c.Name)
	}
	assert.Equal(t, []string{"px.UNSPECIFIED"}, names,
		"the shared enum value collides; the enums themselves and the shared package do not")
	assert.Equal(t, []collide.Claim{
		{Module: "alpha", File: "px/alpha.proto"},
		{Module: "beta", File: "px/beta.proto"},
	}, got[0].Claims)
}

// TestSharedPackageAloneIsNotACollision is the false positive the previous
// test would create if package names were compared as symbols: enum_alpha
// and enum_beta both declare `package px`, which is legal, and two modules
// sharing a Protobuf package must stay clean on their own.
func TestSharedPackageAloneIsNotACollision(t *testing.T) {
	t.Parallel()

	got, err := collide.Check(t.Context(), []collide.Module{
		{Name: "alpha", Root: "testdata/enum_alpha", Paths: []string{"px/alpha.proto"}},
		{Name: "voya", Root: "testdata/voya_vendored", Paths: []string{"voya/service.proto"}},
	})
	require.NoError(t, err)
	assert.Empty(t, got)
}

// TestPackageMeetingADeclarationCollides pins RegisterFile's other
// rejection: it keeps packages and declarations in one namespace, so
// pkg_owner's message foo.Bar and pkg_user's `package foo.Bar` cannot
// coexist. Registering them in either order fails — "package name conflict
// over foo.Bar" one way, "name conflict over foo.Bar" the other.
func TestPackageMeetingADeclarationCollides(t *testing.T) {
	t.Parallel()

	got, err := collide.Check(t.Context(), []collide.Module{
		{Name: "owner", Root: "testdata/pkg_owner"},
		{Name: "user", Root: "testdata/pkg_user"},
	})
	require.NoError(t, err)
	require.Len(t, got, 1, "only foo.Bar collides; the shared ancestor package foo does not")
	assert.Equal(t, collide.KindSymbol, got[0].Kind)
	assert.Equal(t, "foo.Bar", got[0].Name)
	assert.Equal(t, []collide.Claim{
		{Module: "owner", File: "foo/bar.proto"},
		{Module: "user", File: "foo/bar/baz.proto"},
	}, got[0].Claims)
}

// TestDuplicateWithinOneModuleCollides pins that a module's own build does
// not reject this. protocompile links each file separately, so px/one.proto
// and px/two.proto both declare px.Dup without error, and RegisterFile then
// panics on the second. Skipping names claimed by only one module reported
// this clean.
func TestDuplicateWithinOneModuleCollides(t *testing.T) {
	t.Parallel()

	got, err := collide.Check(t.Context(), []collide.Module{
		{Name: "dup", Root: "testdata/dup_within"},
	})
	require.NoError(t, err, "the module compiles; the duplicate is invisible until registration")
	require.Len(t, got, 1)
	assert.Equal(t, "px.Dup", got[0].Name)
	assert.Equal(t, []collide.Claim{
		{Module: "dup", File: "px/one.proto"},
		{Module: "dup", File: "px/two.proto"},
	}, got[0].Claims)
	assert.Contains(t, got[0].String(), `symbol "px.Dup" claimed by 2 files in module dup`,
		"one module claiming a name twice must not render as \"1 module\"")
}

// TestExplicitPathsLimitWhatIsChecked exercises Module.Paths, which no
// other test sets: only the listed files are claimed, and a file left out
// of the list is not.
func TestExplicitPathsLimitWhatIsChecked(t *testing.T) {
	t.Parallel()

	// Both roots hold pxf/annotations.proto, but voya_vendored's copy is
	// not listed, so nothing claims it twice.
	got, err := collide.Check(t.Context(), []collide.Module{
		{Name: "chameleon", Root: "testdata/chameleon"},
		{Name: "voya", Root: "testdata/voya_vendored", Paths: []string{"voya/service.proto"}},
	})
	require.NoError(t, err)
	assert.Empty(t, got)

	// Repeating a path claims it once, not twice.
	got, err = collide.Check(t.Context(), []collide.Module{
		{Name: "chameleon", Root: "testdata/chameleon", Paths: []string{"pxf/annotations.proto", "pxf/annotations.proto"}},
		{Name: "voya", Root: "testdata/voya", Paths: []string{"voya/service.proto"}},
	})
	require.NoError(t, err)
	assert.Empty(t, got)
}

// TestSymlinkedRootIsWalked covers a root that is a symlink to a directory
// — a sibling checkout or a cached module directory. filepath.WalkDir
// lstats its root, so the symlink walked as a single non-directory entry
// and yielded no files at all, and the module was reported clean.
func TestSymlinkedRootIsWalked(t *testing.T) {
	t.Parallel()

	target, err := filepath.Abs("testdata/voya_vendored")
	require.NoError(t, err)
	link := filepath.Join(t.TempDir(), "voya")
	require.NoError(t, os.Symlink(target, link))

	got, err := collide.Check(t.Context(), []collide.Module{
		{Name: "chameleon", Root: "testdata/chameleon"},
		{Name: "voya", Root: link},
	})
	require.NoError(t, err)
	require.NotEmpty(t, got, "a symlinked root must be checked, not silently skipped")
	assert.Equal(t, "pxf/annotations.proto", byKind(got, collide.KindFile)[0].Name)
}

// TestRootWithNoProtosIsAnError keeps a root that yields nothing — a typo
// that happens to name an existing directory, or a checkout that has not
// been populated — from being reported as cleared.
func TestRootWithNoProtosIsAnError(t *testing.T) {
	t.Parallel()

	empty := t.TempDir()
	_, err := collide.Check(t.Context(), []collide.Module{
		{Name: "chameleon", Root: "testdata/chameleon"},
		{Name: "empty", Root: empty},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "contains no .proto files")

	file := filepath.Join(t.TempDir(), "notadir")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o600))
	_, err = collide.Check(t.Context(), []collide.Module{{Name: "f", Root: file}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not a directory")
}

// TestSameRootTwiceIsAnError keeps a copy-paste in a CI invocation from
// reporting every file in one root as claimed by two modules.
func TestSameRootTwiceIsAnError(t *testing.T) {
	t.Parallel()

	_, err := collide.Check(t.Context(), []collide.Module{
		{Name: "a", Root: "testdata/chameleon"},
		{Name: "b", Root: "testdata/chameleon"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "have the same root")
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
