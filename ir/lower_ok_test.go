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

	"github.com/trendvidia/protocompile/ast"
	"github.com/trendvidia/protocompile/incremental"
	"github.com/trendvidia/protocompile/incremental/queries"
	"github.com/trendvidia/protocompile/ir"
	"github.com/trendvidia/protocompile/parser"
	"github.com/trendvidia/protocompile/report"
	"github.com/trendvidia/protocompile/source"
)

// TestLowerOKFollowsSeverity pins what [ir.Session.Lower]'s ok return
// means: lowering succeeded without errors. It is the same pin as
// TestParseOKFollowsSeverity in the parser, on the same comparison, and
// it was wrong the same way — a file whose only lowering diagnostic is a
// warning reported failure, while a recovered ICE reported success. No
// in-tree caller acts on the value (both discard it), so nothing else
// notices; callers of the exported API get the wrong answer directly.
func TestLowerOKFollowsSeverity(t *testing.T) {
	t.Parallel()

	// A `reserved 5 to 5;` range is a warning at lowering time and
	// nothing more.
	const src = `syntax = "proto3";
package test;
message M {
  reserved 5 to 5;
  string s = 1;
}
`

	sess := new(ir.Session)
	importer := descriptorProtoImporter(t, sess)

	var rep report.Report
	astFile, ok := parser.Parse("x.proto", source.NewFile("x.proto", src), &rep)
	require.True(t, ok, "fixture must parse: %v", rep.Diagnostics)

	prior := len(rep.Diagnostics)
	_, ok = sess.Lower(astFile, &rep, importer)

	lowered := rep.Diagnostics[prior:]
	require.NotEmpty(t, lowered, "fixture must produce a diagnostic to be meaningful")
	for _, d := range lowered {
		require.Greater(t, int(d.Level()), int(report.Error),
			"fixture must produce nothing worse than a warning, got: %s", d.Message())
	}
	assert.True(t, ok, "a warnings-only lowering succeeded without errors")
}

// descriptorProtoImporter returns an [ir.Importer] serving the well-known
// descriptor.proto, lowered into the same session. Lowering requests it
// implicitly for every file, and an importer that hands back a nil file
// panics the lowerer.
func descriptorProtoImporter(t *testing.T, sess *ir.Session) ir.Importer {
	t.Helper()
	results, _, err := incremental.Run(t.Context(), incremental.New(), queries.IR{
		Opener:  source.WKTs(),
		Session: sess,
		Path:    ir.DescriptorProtoPath,
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.NotNil(t, results[0].Value)
	dp := results[0].Value

	return func(_ int, path string, _ ast.DeclImport) (*ir.File, error) {
		require.Equal(t, ir.DescriptorProtoPath, path, "fixture imports nothing else")
		return dp, nil
	}
}
