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
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trendvidia/protocompile/ast"
	"github.com/trendvidia/protocompile/incremental"
	"github.com/trendvidia/protocompile/incremental/queries"
	"github.com/trendvidia/protocompile/internal/ext/mapsx"
	"github.com/trendvidia/protocompile/internal/intern"
	"github.com/trendvidia/protocompile/ir"
	"github.com/trendvidia/protocompile/report"
	"github.com/trendvidia/protocompile/seq"
	"github.com/trendvidia/protocompile/source"
)

func TestImportResolution(t *testing.T) {
	t.Parallel()

	s := new(ir.Session)

	// Below, -> is a normal import, => is public, and ~> is weak.

	// a
	a := buildFile(s, "a.proto")
	assert.Empty(t, directs(a))
	assert.Empty(t, transitive(a))

	// a -> b
	b := buildFile(s, "b.proto", ir.Import{File: a})
	assert.Equal(t, []imp{{path: "a.proto"}}, directs(b))
	assert.Equal(t, []imp{{path: "a.proto"}}, transitive(b))

	// a -> b -
	// \        \
	//   -> c -> d
	c := buildFile(s, "c.proto", ir.Import{File: a})
	d := buildFile(s, "d.proto", ir.Import{File: b}, ir.Import{File: c})
	assert.Equal(t, []imp{{path: "b.proto"}, {path: "c.proto"}}, directs(d))
	assert.Equal(t, []imp{{path: "b.proto"}, {path: "c.proto"}, {path: "a.proto"}}, transitive(d))

	// a => e
	e := buildFile(s, "e.proto", ir.Import{File: a, Public: true})
	assert.Equal(t, []imp{{path: "a.proto", public: true}}, directs(e))
	assert.Equal(t, []imp{{path: "a.proto", public: true}}, transitive(e))

	// a => e -> f
	f := buildFile(s, "f.proto", ir.Import{File: e})
	assert.Equal(t, []imp{{path: "e.proto"}}, directs(f))
	assert.Equal(t, []imp{{path: "e.proto"}, {path: "a.proto"}}, transitive(f))

	// a => e => g
	g := buildFile(s, "g.proto", ir.Import{File: e, Public: true})
	assert.Equal(t, []imp{{path: "e.proto", public: true}}, directs(g))
	assert.Equal(t, []imp{{path: "e.proto", public: true}, {path: "a.proto", public: true}}, transitive(g))

	// a -> b -
	// \        \
	//   -> c -> d => h
	h := buildFile(s, "h.proto", ir.Import{File: d, Public: true})
	assert.Equal(t, []imp{{path: "d.proto", public: true}}, directs(h))
	assert.Equal(t, []imp{{path: "d.proto", public: true}, {path: "b.proto"}, {path: "c.proto"}, {path: "a.proto"}}, transitive(h))

	// This test case is particularly important, because it validates that
	// in a diamond configuration, we don't lose public-ness of a transitive
	// import if there are multiple public and non-public paths.
	//
	// a => i => j
	// \        /
	//   -> c -
	i := buildFile(s, "i.proto", ir.Import{File: a, Public: true})
	j := buildFile(s, "j.proto", ir.Import{File: c}, ir.Import{File: i, Public: true})
	assert.Equal(t, []imp{{path: "i.proto", public: true}, {path: "c.proto"}}, directs(j))
	assert.Equal(t, []imp{{path: "i.proto", public: true}, {path: "c.proto"}, {path: "a.proto", public: true}}, transitive(j))
	// Same as above but order is swapped.
	j2 := buildFile(s, "j.proto", ir.Import{File: i, Public: true}, ir.Import{File: c})
	assert.Equal(t, []imp{{path: "i.proto", public: true}, {path: "c.proto"}}, directs(j2))
	assert.Equal(t, []imp{{path: "i.proto", public: true}, {path: "c.proto"}, {path: "a.proto", public: true}}, transitive(j2))

	// a ~> k
	// NOTE: weak imports are not tracked transitively.
	k := buildFile(s, "k.proto", ir.Import{File: a, Weak: true})
	assert.Equal(t, []imp{{path: "a.proto", weak: true}}, directs(k))
	assert.Equal(t, []imp{{path: "a.proto"}}, transitive(k))

	// a ~> k ~> l
	l := buildFile(s, "l.proto", ir.Import{File: k, Weak: true})
	assert.Equal(t, []imp{{path: "k.proto", weak: true}}, directs(l))
	assert.Equal(t, []imp{{path: "k.proto"}, {path: "a.proto"}}, transitive(l))
}

// buildFile implements the most basic rudiments of building a File with a
// given import set, without involving an AST.
// TestImportPublicPaths checks that a name is visible when any path to it is
// public from a direct import, however else that file is reached (issue #300).
// protoc 36.2 gives the same answer for each case.
func TestImportPublicPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		a       []string // The import lines of a.proto.
		visible bool
	}{
		{
			name:    "private path first",
			a:       []string{`import public "priv.proto";`, `import public "mid.proto";`},
			visible: true,
		},
		{
			name:    "private path last",
			a:       []string{`import public "mid.proto";`, `import public "priv.proto";`},
			visible: true,
		},
		{
			name:    "private import of the file first",
			a:       []string{`import "leaf.proto";`, `import public "mid.proto";`},
			visible: true,
		},
		{
			name:    "private import of the file last",
			a:       []string{`import public "mid.proto";`, `import "leaf.proto";`},
			visible: true,
		},
		{
			name:    "weak import of the file",
			a:       []string{`import weak "leaf.proto";`, `import public "mid.proto";`},
			visible: true,
		},
		{
			name:    "private import of the public path's head",
			a:       []string{`import "priv.proto";`, `import public "mid.proto";`},
			visible: true,
		},
		{
			name:    "one more public hop",
			a:       []string{`import public "fwd.proto";`},
			visible: true,
		},
		{
			name: "only a private path",
			a:    []string{`import public "priv.proto";`},
		},
		{
			name: "only a private import of the file",
			a:    []string{`import "leaf.proto";`},
		},
		{
			name: "public path behind a private import",
			a:    []string{`import "mid.proto";`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			header := "syntax = \"proto3\";\npackage p;\n"
			sources := map[string]string{
				"leaf.proto": "syntax = \"proto3\";\npackage p.sub2;\nmessage S {}\n",
				"mid.proto":  "syntax = \"proto3\";\npackage p.sub;\nimport public \"leaf.proto\";\n",
				"priv.proto": header + "import \"mid.proto\";\n",
				"fwd.proto": header + "import public \"priv.proto\";\n" +
					"import public \"mid.proto\";\n",
				"a.proto": header + strings.Join(tt.a, "\n") + "\n",
				"c.proto": header + "import \"a.proto\";\nmessage C { sub2.S s = 1; }\n",
			}
			files := make(map[string]*source.File, len(sources))
			for path, text := range sources {
				files[path] = source.NewFile(path, text)
			}

			results, r, err := incremental.Run(t.Context(), incremental.New(), queries.Link{
				Opener:    &source.Openers{source.NewMap(files), source.WKTs()},
				Session:   new(ir.Session),
				Workspace: source.NewWorkspace("c.proto"),
			})
			require.NoError(t, err)
			require.Len(t, results, 1)

			var errors []string
			for _, d := range r.Diagnostics {
				if d.Level() <= report.Error {
					errors = append(errors, d.Message())
				}
			}

			if !tt.visible {
				assert.Equal(t, []string{"cannot find `sub2.S` in this scope"}, errors)
				return
			}

			assert.Empty(t, errors)
			require.Len(t, results[0].Value, 1)
			c := results[0].Value[0].FindSymbol("p.C").AsType()
			field := c.Members().At(0)
			assert.Equal(t, ir.FullName("p.sub2.S"), field.Element().FullName())
		})
	}
}

func buildFile(
	session *ir.Session,
	path string,
	imports ...ir.Import,
) *ir.File {
	file := ir.NewFile(session, path)
	table := ir.GetImports(file)

	dedup := make(intern.Map[ast.DeclImport])
	for _, imp := range imports {
		if !mapsx.AddZero(dedup, imp.File.InternedPath()) {
			continue
		}

		table.AddDirect(imp)
	}
	table.Recurse(dedup)
	table.Insert(ir.Import{}, -1, false) // Dummy descriptor.proto.

	return file
}

type imp struct {
	path         string
	public, weak bool
}

func directs(f *ir.File) []imp {
	return slices.Collect(seq.Map(ir.GetImports(f).Directs(), func(i ir.Import) imp {
		return imp{i.File.Path(), i.Public, i.Weak}
	}))
}

func transitive(f *ir.File) []imp {
	return slices.Collect(seq.Map(ir.GetImports(f).Transitive(), func(i ir.Import) imp {
		return imp{i.File.Path(), i.Public, i.Weak}
	}))
}
