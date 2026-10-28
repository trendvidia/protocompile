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

package experimentalcompile_test

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trendvidia/protocompile"
	"github.com/trendvidia/protocompile/fdp"
	"github.com/trendvidia/protocompile/incremental"
	"github.com/trendvidia/protocompile/incremental/queries"
	"github.com/trendvidia/protocompile/ir"
	"github.com/trendvidia/protocompile/report"
	"github.com/trendvidia/protocompile/reporter"
	"github.com/trendvidia/protocompile/source"
)

// cyclicDescriptor returns protobuf's descriptor.proto with an import of
// field_mask.proto added. field_mask.proto imports descriptor.proto
// implicitly, so lowering field_mask.proto finds its implicit import in a
// cycle. That crashed its lowering (an ICE) and left the file partly
// built.
func cyclicDescriptor(t *testing.T) string {
	t.Helper()
	f, err := source.WKTs().Open(ir.DescriptorProtoPath)
	require.NoError(t, err)
	text := f.Text()
	vendored := strings.Replace(text, "package google.protobuf;",
		"package google.protobuf;\nimport \"google/protobuf/field_mask.proto\";", 1)
	require.NotEqual(t, text, vendored)
	return vendored
}

const crashUser = `syntax = "proto3"; package m; import "google/protobuf/descriptor.proto"; message M { int32 n = 1; }`

// A cycle through the implicit descriptor.proto import is reported as the
// user's import cycle. Before, it crashed lowering, and the partly built
// file reached descriptor generation, which dereferenced builtins that
// were never set, in every run (protocompile#253).
func TestLoweringCrash_DescriptorSet(t *testing.T) {
	t.Parallel()
	vendored := cyclicDescriptor(t)
	for range 20 {
		files := source.NewMap(nil)
		files.Add(ir.DescriptorProtoPath, vendored)
		files.Add("m.proto", crashUser)
		chain := source.Openers{files, source.WKTs()}
		results, r, err := incremental.Run(t.Context(), incremental.New(), queries.FDS{
			Opener:    &chain,
			Session:   &ir.Session{},
			Workspace: source.NewWorkspace("m.proto"),
			Options:   fdp.Options{},
		})
		require.NoError(t, err, "the run must end in diagnostics, not a panic")
		require.Len(t, results, 1)
		var errs []string
		for i := range r.Diagnostics {
			d := &r.Diagnostics[i]
			assert.NotEqual(t, report.ICE, d.Level(), "no internal compiler error: %s", d.Message())
			if d.Level() == report.Error {
				errs = append(errs, d.Message())
			}
		}
		assert.Contains(t, errs, `detected cyclic import while importing "google/protobuf/descriptor.proto"`)
		assert.Contains(t, errs, `"google/protobuf/field_mask.proto" imports "google/protobuf/descriptor.proto" implicitly, which closes the cycle`)
	}
}

// The same input through Compiler.Compile fails with errors, not a panic.
func TestLoweringCrash_Compile(t *testing.T) {
	t.Parallel()
	vendored := cyclicDescriptor(t)
	var errs []string
	c := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(protocompile.ResolverFunc(func(path string) (protocompile.SearchResult, error) {
			switch path {
			case ir.DescriptorProtoPath:
				return protocompile.SearchResult{Source: io.NopCloser(strings.NewReader(vendored))}, nil
			case "m.proto":
				return protocompile.SearchResult{Source: io.NopCloser(strings.NewReader(crashUser))}, nil
			}
			return protocompile.SearchResult{}, os.ErrNotExist
		})),
		Reporter: reporter.NewReporter(
			func(err reporter.ErrorWithPos) error { errs = append(errs, err.Error()); return nil },
			nil,
		),
	}
	_, err := c.Compile(t.Context(), "m.proto")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "nil pointer")
	require.NotEmpty(t, errs)
}
