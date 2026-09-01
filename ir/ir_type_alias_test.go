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

package ir_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trendvidia/protocompile/ir"
	"github.com/trendvidia/protocompile/report"
	"github.com/trendvidia/protocompile/seq"
)

// TestTypeAliasSymbol verifies B1-equivalent wiring for `type`
// alias declarations: each `type X = Base` shows up as an
// [ir.TypeAlias] in [(*ir.File).TypeAliases()], registers under
// [ir.SymbolKindTypeAlias] in the exported symbol table,
// preserves the base-type text, and round-trips via
// [Symbol.AsTypeAlias].
func TestTypeAliasSymbol(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

message Address {}

type Email   = string;
type Phone   = string;
type Contact = test.Address;
`

	file, _ := compileForAnnotationTest(t, src)
	require.NotNil(t, file)

	require.Equal(t, 3, file.TypeAliases().Len())

	var aliases []ir.TypeAlias
	for a := range seq.Values(file.TypeAliases()) {
		aliases = append(aliases, a)
	}

	expectedNames := []string{"Email", "Phone", "Contact"}
	expectedFQNs := []string{"test.Email", "test.Phone", "test.Contact"}
	expectedBases := []string{"string", "string", "test.Address"}

	for i, a := range aliases {
		assert.Equal(t, expectedNames[i], a.Name(), "alias %d name", i)
		assert.Equal(t, expectedFQNs[i], string(a.FullName()), "alias %d FQN", i)
		assert.Equal(t, expectedBases[i], a.BaseTypeName(), "alias %d base", i)
		assert.False(t, a.AST().IsZero(), "alias %d AST link", i)
	}

	for i, fqn := range expectedFQNs {
		sym := file.FindSymbol(ir.FullName(fqn))
		require.False(t, sym.IsZero(), "missing symbol for %s", fqn)
		assert.Equal(t, ir.SymbolKindTypeAlias, sym.Kind(), "symbol kind for %s", fqn)
		assert.Equal(t, aliases[i], sym.AsTypeAlias(), "AsTypeAlias round-trip for %s", fqn)
	}
}

// TestTypeAliasBaseFQN verifies base-type resolution for the FDP
// carrier: [ir.TypeAlias.BaseTypeFQN] returns the fully-qualified
// name of the resolved base — including for bare in-package
// spellings and chained aliases — while primitives keep their
// predeclared name and [ir.TypeAlias.BaseTypeName] keeps the
// as-written text. Unresolved bases fall back to the as-written
// text.
func TestTypeAliasBaseFQN(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

message Address {}

enum Status {
  STATUS_UNSPECIFIED = 0;
}

type Email    = string;
type Loc      = Address;
type Contact  = test.Address;
type Settled  = Status;
type Chain    = Email;
type Broken   = DoesNotExist;
`

	file, _ := compileForAnnotationTest(t, src)
	require.NotNil(t, file)

	wantFQN := map[string]string{
		"test.Email":   "string",
		"test.Loc":     "test.Address",
		"test.Contact": "test.Address",
		"test.Settled": "test.Status",
		"test.Chain":   "test.Email",
		"test.Broken":  "DoesNotExist",
	}
	wantWritten := map[string]string{
		"test.Email":   "string",
		"test.Loc":     "Address",
		"test.Contact": "test.Address",
		"test.Settled": "Status",
		"test.Chain":   "Email",
		"test.Broken":  "DoesNotExist",
	}

	require.Equal(t, len(wantFQN), file.TypeAliases().Len())
	for a := range seq.Values(file.TypeAliases()) {
		fqn := string(a.FullName())
		assert.Equal(t, wantFQN[fqn], a.BaseTypeFQN(), "%s resolved base", fqn)
		assert.Equal(t, wantWritten[fqn], a.BaseTypeName(), "%s as-written base", fqn)
	}
}

// TestTypeAliasBaseFQNCrossFile verifies that an alias whose base
// lives in an imported file resolves to the base's FQN in the
// defining file's package.
func TestTypeAliasBaseFQNCrossFile(t *testing.T) {
	t.Parallel()

	const lib = `syntax = "proto3";
package fixtures.lib;

type Email = string;
`
	const main = `syntax = "proto3";
package fixtures.main;

import "lib.proto";

type CompanyEmail = fixtures.lib.Email;
`

	file, rep := compileTwoForAnnotationTest(t, main, lib)
	for _, d := range rep.Diagnostics {
		if isError(d) {
			t.Errorf("unexpected diagnostic: %s", d.Message())
		}
	}

	require.Equal(t, 1, file.TypeAliases().Len())
	a := file.TypeAliases().At(0)
	assert.Equal(t, "fixtures.main.CompanyEmail", string(a.FullName()))
	assert.Equal(t, "fixtures.lib.Email", a.BaseTypeFQN())
}

// countErrorsContaining tallies error-level diagnostics whose message
// contains all needles.
func countErrorsContaining(rep *report.Report, needles ...string) int {
	var n int
outer:
	for _, d := range rep.Diagnostics {
		if !isError(d) {
			continue
		}
		for _, needle := range needles {
			if !strings.Contains(d.Message(), needle) {
				continue outer
			}
		}
		n++
	}
	return n
}

// TestTypeAliasBrokenBaseDiagnosedAtDeclaration verifies issue #129:
// an alias whose base does not resolve is diagnosed at the
// declaration site — exactly once — whether or not any field
// references it.
func TestTypeAliasBrokenBaseDiagnosedAtDeclaration(t *testing.T) {
	t.Parallel()

	t.Run("unreferenced", func(t *testing.T) {
		t.Parallel()
		const src = `syntax = "proto3";
package test;

type Broken = DoesNotExist;
`
		_, rep := compileForAnnotationTest(t, src)
		assert.Equal(t, 1, countErrorsContaining(rep, "DoesNotExist"),
			"want exactly one declaration-site error, got: %v", rep.Diagnostics)
	})

	t.Run("referenced", func(t *testing.T) {
		t.Parallel()
		const src = `syntax = "proto3";
package test;

type Broken = DoesNotExist;

message M {
  Broken a = 1;
  Broken b = 2;
}
`
		_, rep := compileForAnnotationTest(t, src)
		assert.Equal(t, 1, countErrorsContaining(rep, "DoesNotExist"),
			"multiple use sites must not multiply the diagnostic, got: %v", rep.Diagnostics)
	})
}

// TestTypeAliasCycleDiagnosedAtDeclaration verifies issue #129 for
// cycles: a cyclic alias chain is diagnosed at the declaration site,
// exactly once per cycle, referenced or not. Self-cycles count.
func TestTypeAliasCycleDiagnosedAtDeclaration(t *testing.T) {
	t.Parallel()

	t.Run("unreferenced pair", func(t *testing.T) {
		t.Parallel()
		const src = `syntax = "proto3";
package test;

type A = B;
type B = A;
`
		_, rep := compileForAnnotationTest(t, src)
		assert.Equal(t, 1, countErrorsContaining(rep, "is cyclic"),
			"want exactly one cycle error, got: %v", rep.Diagnostics)
	})

	t.Run("self", func(t *testing.T) {
		t.Parallel()
		const src = `syntax = "proto3";
package test;

type Selfish = Selfish;
`
		_, rep := compileForAnnotationTest(t, src)
		assert.Equal(t, 1, countErrorsContaining(rep, "is cyclic"),
			"want exactly one cycle error, got: %v", rep.Diagnostics)
	})

	t.Run("referenced", func(t *testing.T) {
		t.Parallel()
		const src = `syntax = "proto3";
package test;

type A = test.B;
type B = test.A;

message M {
  A a = 1;
  B b = 2;
}
`
		_, rep := compileForAnnotationTest(t, src)
		assert.Equal(t, 1, countErrorsContaining(rep, "is cyclic"),
			"use sites must not multiply the cycle error, got: %v", rep.Diagnostics)
	})

	t.Run("tail into cycle", func(t *testing.T) {
		t.Parallel()
		// C drains into the A<->B cycle but is not part of it; the
		// cycle reports once and C itself is not called cyclic.
		const src = `syntax = "proto3";
package test;

type A = B;
type B = A;
type C = A;
`
		_, rep := compileForAnnotationTest(t, src)
		assert.Equal(t, 1, countErrorsContaining(rep, "is cyclic"),
			"want exactly one cycle error, got: %v", rep.Diagnostics)
	})
}

// TestTypeAliasBrokenBaseCrossFile verifies the declaration-site
// diagnostic surfaces when the broken alias lives in an imported
// file: the dependency's compile reports it, and the importing
// file's use sites do not duplicate it.
func TestTypeAliasBrokenBaseCrossFile(t *testing.T) {
	t.Parallel()

	const lib = `syntax = "proto3";
package fixtures.lib;

type Broken = DoesNotExist;
`
	const main = `syntax = "proto3";
package fixtures.main;

import "lib.proto";

message M {
  fixtures.lib.Broken a = 1;
}
`

	_, rep := compileTwoForAnnotationTest(t, main, lib)
	assert.Equal(t, 1, countErrorsContaining(rep, "DoesNotExist"),
		"want exactly one error from the declaring file's compile, got: %v", rep.Diagnostics)
}
