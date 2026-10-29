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

	"github.com/trendvidia/protocompile/incremental"
	"github.com/trendvidia/protocompile/incremental/queries"
	"github.com/trendvidia/protocompile/ir"
	"github.com/trendvidia/protocompile/report"
	"github.com/trendvidia/protocompile/source"
)

// TestTypeAliasRuleCheckedAtAlias pins #287: a type alias's rule is checked
// once, at the alias, whether or not a field uses it, and `this` binds to
// one value of the alias's base type.
func TestTypeAliasRuleCheckedAtAlias(t *testing.T) {
	t.Parallel()

	const header = `syntax = "proto3";
package p;
annotation validate(rule: expression);
`
	tests := []struct {
		name   string
		files  map[string]string // "x.proto" is compiled; others are imports.
		want   int               // error diagnostics
		inFile string            // when set, every error must point into this file
	}{
		{
			name:  "unused alias",
			files: map[string]string{"x.proto": header + "type Name = string @validate(nope(this));\n"},
			want:  1,
		},
		{
			name: "alias used by two fields",
			files: map[string]string{"x.proto": header + `type Name = string @validate(nope(this));
message M {
  Name n = 1;
  Name o = 2;
}
`},
			want: 1,
		},
		{
			name: "chain: each link once",
			files: map[string]string{"x.proto": header + `type Name = string @validate(nope(this));
type Short = Name @validate(also_nope(this));
message M {
  Short a = 1;
  Short b = 2;
  Name c = 3;
}
`},
			want: 2,
		},
		{
			name: "alias from an import, used here",
			files: map[string]string{
				"lib.proto": header + "type Name = string @validate(nope(this));\n",
				"x.proto": `syntax = "proto3";
package p;
import "lib.proto";
message M {
  Name n = 1;
  Name o = 2;
}
`,
			},
			// Once, at the alias in lib.proto, not once per field here.
			want:   1,
			inFile: "lib.proto",
		},
		{
			name: "alias rule binds this to one element on a repeated field",
			files: map[string]string{"x.proto": header + `type Flag = bool @validate(this);
message M {
  repeated Flag flags = 1;
}
`},
			want: 0,
		},
		{
			name: "the field's own rule on that repeated field still binds the list",
			files: map[string]string{"x.proto": header + `type Flag = bool;
message M {
  repeated Flag flags = 1 @validate(this);
}
`},
			want: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			files := map[string]*source.File{}
			for path, text := range tt.files {
				files[path] = source.NewFile(path, text)
			}
			results, rep, err := incremental.Run(t.Context(), incremental.New(), queries.IR{
				Opener:  &source.Openers{source.NewMap(files), source.WKTs()},
				Session: new(ir.Session),
				Path:    "x.proto",
			})
			require.NoError(t, err)
			require.Len(t, results, 1)

			var errs []string
			for _, d := range rep.Diagnostics {
				if d.Level() <= report.Error {
					errs = append(errs, d.Message())
					if tt.inFile != "" {
						assert.Equal(t, tt.inFile, d.Primary().Path(), "error location")
					}
				}
			}
			assert.Len(t, errs, tt.want, "errors:\n%s", strings.Join(errs, "\n"))
		})
	}
}

// TestTypeAliasRuleErrorIsAtTheAlias checks that the one error an unused
// alias's rule gets points at the alias's own rule.
func TestTypeAliasRuleErrorIsAtTheAlias(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package p;
annotation validate(rule: expression);
type Name = string @validate(nope(this));
`
	_, rep := compileForAnnotationTest(t, src)
	var found bool
	for _, d := range rep.Diagnostics {
		if d.Level() > report.Error {
			continue
		}
		require.False(t, found, "more than one error: %v", rep.Diagnostics)
		found = true
		assert.Contains(t, d.Message(), "unknown function `nope`")
		span := d.Primary()
		assert.Equal(t, "nope", span.Text())
	}
	assert.True(t, found, "the unused alias's rule was not checked")
}
