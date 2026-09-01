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

package parser_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trendvidia/protocompile/parser"
	"github.com/trendvidia/protocompile/report"
	"github.com/trendvidia/protocompile/source"
)

// TestParseOKFollowsSeverity pins what [parser.Parse]'s ok return means:
// "parsing succeeded without errors", per its doc comment. Warnings and
// remarks are not errors — [report.Diagnostic] says so outright — so they
// must not make it false, and an ICE must.
//
// [report.Level] runs most-severe-first (ICE(1), Error(2), Warning(3),
// Remark(4)), which makes the comparison easy to write backwards. It was:
// until this pin, a proto2 file with a required field, or any file missing
// a `package` declaration, reported a parse failure through the exported
// API while a recovered compiler panic reported success.
func TestParseOKFollowsSeverity(t *testing.T) {
	t.Parallel()

	t.Run("warnings_only", func(t *testing.T) {
		t.Parallel()
		// `required` is diagnosed as a warning, nothing more.
		const src = `syntax = "proto2";
package test;
message M {
  required string s = 1;
}
`
		var rep report.Report
		_, ok := parser.Parse("x.proto", source.NewFile("x.proto", src), &rep)

		require.NotEmpty(t, rep.Diagnostics, "fixture must produce a diagnostic to be meaningful")
		for _, d := range rep.Diagnostics {
			require.Greater(t, int(d.Level()), int(report.Error),
				"fixture must produce nothing worse than a warning, got: %s", d.Message())
		}
		assert.True(t, ok, "a warnings-only parse succeeded without errors")
	})

	t.Run("error", func(t *testing.T) {
		t.Parallel()
		const src = `syntax = "proto3";
package test;
message M {
`
		var rep report.Report
		_, ok := parser.Parse("x.proto", source.NewFile("x.proto", src), &rep)

		var sawError bool
		for _, d := range rep.Diagnostics {
			if d.Level() <= report.Error {
				sawError = true
			}
		}
		require.True(t, sawError, "fixture must produce an error to be meaningful")
		assert.False(t, ok, "a parse that errored did not succeed")
	})
}
