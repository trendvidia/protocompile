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
