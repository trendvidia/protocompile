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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trendvidia/protocompile/ir"
	"github.com/trendvidia/protocompile/seq"
)

// TestFunctionSymbol verifies B1-equivalent wiring for `function`
// declarations: each `function foo(...)` shows up as an
// [ir.Function] in [(*ir.File).Functions()], gets registered under
// [ir.SymbolKindFunction] in the file's exported symbol table,
// parameter list is materialised in source order with textual
// types, and [Symbol.AsFunction] round-trips.
func TestFunctionSymbol(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

function is_e164();
function matches(value: string, pattern: string);
function compare(a: any, b: any);
`

	file, _ := compileForAnnotationTest(t, src)
	require.NotNil(t, file)

	require.Equal(t, 3, file.Functions().Len())

	var fns []ir.Function
	for f := range seq.Values(file.Functions()) {
		fns = append(fns, f)
	}

	expectedNames := []string{"is_e164", "matches", "compare"}
	expectedFQNs := []string{"test.is_e164", "test.matches", "test.compare"}
	expectedParams := [][]struct{ name, typ string }{
		nil,
		{{"value", "string"}, {"pattern", "string"}},
		{{"a", "any"}, {"b", "any"}},
	}

	for i, fn := range fns {
		assert.Equal(t, expectedNames[i], fn.Name(), "function %d name", i)
		assert.Equal(t, expectedFQNs[i], string(fn.FullName()), "function %d FQN", i)
		assert.False(t, fn.AST().IsZero(), "function %d AST link", i)

		params := fn.Params()
		assert.Equal(t, len(expectedParams[i]), params.Len(), "function %d param count", i)
		var idx int
		for p := range seq.Values(params) {
			assert.Equal(t, expectedParams[i][idx].name, p.Name(), "function %d param %d name", i, idx)
			assert.Equal(t, expectedParams[i][idx].typ, p.TypeName(), "function %d param %d type", i, idx)
			assert.Equal(t, fn, p.Function(), "function %d param %d parent backref", i, idx)
			idx++
		}
	}

	// Symbol-table round-trip.
	for i, fqn := range expectedFQNs {
		sym := file.FindSymbol(ir.FullName(fqn))
		require.False(t, sym.IsZero(), "missing symbol for %s", fqn)
		assert.Equal(t, ir.SymbolKindFunction, sym.Kind(), "symbol kind for %s", fqn)
		assert.Equal(t, fns[i], sym.AsFunction(), "AsFunction round-trip for %s", fqn)
	}
}

// TestFunctionParamTypeFQN verifies parameter-type resolution for the
// FDP carrier (issue #294): [ir.FunctionParam.TypeFQN] is the full
// name of a message, enum or alias however it was spelled, an alias
// stays itself rather than unwrapping to its base, and scalars and
// `any` keep their names. [ir.FunctionParam.TypeName] keeps the
// as-written text.
func TestFunctionParamTypeFQN(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test.sub;

message Small {
  message Inner {}
}
enum Color { COLOR_UNSPECIFIED = 0; }

type Smaller = Small;
type Chain   = Smaller;

function f(
  a: string,
  b: any,
  c: Small,
  d: Small.Inner,
  e: Color,
  g: Smaller,
  h: Chain,
  i: sub.Small,
  j: test.sub.Smaller,
  k: .test.sub.Color
);
`

	file, rep := compileForAnnotationTest(t, src)
	require.NotNil(t, file)
	for _, d := range rep.Diagnostics {
		if isError(d) {
			t.Errorf("unexpected diagnostic: %s", d.Message())
		}
	}

	want := []struct{ written, fqn string }{
		{"string", "string"},
		{"any", "any"},
		{"Small", "test.sub.Small"},
		{"Small.Inner", "test.sub.Small.Inner"},
		{"Color", "test.sub.Color"},
		{"Smaller", "test.sub.Smaller"},
		{"Chain", "test.sub.Chain"},
		{"sub.Small", "test.sub.Small"},
		{"test.sub.Smaller", "test.sub.Smaller"},
		{".test.sub.Color", "test.sub.Color"},
	}

	require.Equal(t, 1, file.Functions().Len())
	params := file.Functions().At(0).Params()
	require.Equal(t, len(want), params.Len())
	for i, w := range want {
		assert.Equal(t, w.written, params.At(i).TypeName(), "param %d as written", i)
		assert.Equal(t, w.fqn, params.At(i).TypeFQN(), "param %d resolved", i)
	}
}

// TestFunctionParamTypeFQNCrossFile verifies that a parameter type
// declared in an imported file resolves to its FQN there, and that
// doing so counts as a use of the import.
func TestFunctionParamTypeFQNCrossFile(t *testing.T) {
	t.Parallel()

	const lib = `syntax = "proto3";
package fixtures.lib;

type Email = string;
`
	const main = `syntax = "proto3";
package fixtures.main;

import "lib.proto";

function is_email(v: fixtures.lib.Email);
`

	file, rep := compileTwoForAnnotationTest(t, main, lib)
	for _, d := range rep.Diagnostics {
		t.Errorf("unexpected diagnostic: %s", d.Message())
	}

	require.Equal(t, 1, file.Functions().Len())
	p := file.Functions().At(0).Params().At(0)
	assert.Equal(t, "fixtures.lib.Email", p.TypeFQN())
}

// TestSymbolKindMisuseDiagnosed verifies that a type position naming
// an annotation, function or type alias is an ordinary diagnostic.
// All three kinds used to lack a noun, and describing them panicked.
func TestSymbolKindMisuseDiagnosed(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name, decl string
		needles    []string
	}{
		{"field-of-function", "function g();\nmessage M { g f = 1; }", []string{"expected type", "found function"}},
		{"field-of-annotation", "annotation g();\nmessage M { g f = 1; }", []string{"expected type", "found annotation"}},
		{"method-of-alias", "message R {}\ntype A = R;\nservice S { rpc M(A) returns (R); }", []string{"found type alias"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			src := "syntax = \"proto3\";\npackage test;\n\n" + tt.decl + "\n"
			file, rep := compileForAnnotationTest(t, src)
			require.NotNil(t, file)
			assert.Equal(t, 1, countErrorsContaining(rep, tt.needles...))
		})
	}
}

// TestFunctionParamTypeMustResolve verifies that a parameter type
// that is not a primitive, enum, message or alias is an error, as the
// draft's "Parameter Types" section requires, and that the unresolved
// parameter falls back to its as-written text.
func TestFunctionParamTypeMustResolve(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name, decl string
		needles    []string
	}{
		{"unknown", `function f(v: Nope);`, []string{"cannot find", "Nope"}},
		{"expression", `function f(v: expression);`, []string{"cannot find", "expression"}},
		{"function", "function g();\nfunction f(v: g);", []string{"expected type", "found function"}},
		{"annotation", "annotation g();\nfunction f(v: g);", []string{"expected type", "found annotation"}},
		{"repeated", `function f(v: repeated string);`, []string{"must be a name"}},
		{"map", `function f(v: map<string, string>);`, []string{"must be a name"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			src := "syntax = \"proto3\";\npackage test;\n\n" + tt.decl + "\n"
			file, rep := compileForAnnotationTest(t, src)
			require.NotNil(t, file)
			assert.Equal(t, 1, countErrorsContaining(rep, tt.needles...))
		})
	}
}
