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
