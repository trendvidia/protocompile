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

	"github.com/trendvidia/protocompile/incremental"
	"github.com/trendvidia/protocompile/incremental/queries"
	"github.com/trendvidia/protocompile/ir"
	"github.com/trendvidia/protocompile/seq"
	"github.com/trendvidia/protocompile/source"
)

// TestAnnotationSymbol verifies B1 wiring: an `annotation` declaration
// shows up as an [ir.Annotation] in [(*ir.File).Annotations()], gets
// registered under [ir.SymbolKindAnnotation] in the file's exported
// symbol table, parameter list is materialised in source order, and
// [Symbol.AsAnnotation] round-trips back to the same [ir.Annotation]
// instance.
func TestAnnotationSymbol(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

annotation required;
annotation description(text: string);
annotation example(value: any);
`

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
	require.NoError(t, results[0].Fatal)
	file := results[0].Value
	require.NotNil(t, file)

	annsSeq := file.Annotations()
	require.Equal(t, 3, annsSeq.Len(), "annotation declarations should round-trip from AST to IR")

	var anns []ir.Annotation
	for a := range seq.Values(annsSeq) {
		anns = append(anns, a)
	}

	expectedNames := []string{"required", "description", "example"}
	expectedFQNs := []string{"test.required", "test.description", "test.example"}
	expectedParams := [][]string{nil, {"text"}, {"value"}}

	for i, a := range anns {
		assert.Equal(t, expectedNames[i], a.Name(), "annotation %d name", i)
		assert.Equal(t, expectedFQNs[i], string(a.FullName()), "annotation %d FQN", i)
		assert.False(t, a.AST().IsZero(), "annotation %d AST link", i)
		assert.NotZero(t, a.InternedFullName(), "annotation %d interned FQN", i)

		paramSeq := a.Params()
		assert.Equal(t, len(expectedParams[i]), paramSeq.Len(), "annotation %d param count", i)
		var got []string
		for p := range seq.Values(paramSeq) {
			got = append(got, p.Name())
			assert.False(t, p.AST().IsZero(), "annotation %d param %q AST link", i, p.Name())
			assert.Equal(t, a, p.Annotation(), "annotation %d param %q parent backref", i, p.Name())
		}
		assert.Equal(t, expectedParams[i], got, "annotation %d param names", i)
	}

	// Each declaration must appear in the file's symbol table under
	// SymbolKindAnnotation. Use the file's public FindSymbol — same
	// lookup path use-site resolution will use in Phase B2.
	for i, fqn := range expectedFQNs {
		sym := file.FindSymbol(ir.FullName(fqn))
		require.False(t, sym.IsZero(), "missing symbol for %s", fqn)
		assert.Equal(t, ir.SymbolKindAnnotation, sym.Kind(), "symbol kind for %s", fqn)
		assert.Equal(t, anns[i], sym.AsAnnotation(), "AsAnnotation round-trip for %s", fqn)
	}
}
