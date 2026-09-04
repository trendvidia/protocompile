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

package protoctest_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trendvidia/protocompile/incremental"
	"github.com/trendvidia/protocompile/incremental/queries"
	"github.com/trendvidia/protocompile/internal/protoc"
	"github.com/trendvidia/protocompile/ir"
	"github.com/trendvidia/protocompile/report"
	"github.com/trendvidia/protocompile/source"
)

// TestExponentLiteralsMatchProtoc pins #191 against the oracle.
//
// An exponent makes a literal a float. protoc enforces that: `1e2` is
// rejected as a field number, an enum number and an integer default, and
// accepted as a float default. protocompile accepted all three, because
// the lexer set IsFloat from `.` and `-` alone -- "positive exponents are
// not necessarily floats" -- contradicting the accessor's own doc.
//
// The cases below are the four readers of that flag: field numbers and
// enum numbers (ir/lower_eval.go's integer path), scalar option values
// (its float path and parser/legalize_option.go), and the diagnostic
// classification in internal/taxa that names what was expected.
//
// Both compilers are run on each. The assertion is agreement, not a fixed
// verdict, so protoc remains the authority if it ever changes its mind.
func TestExponentLiteralsMatchProtoc(t *testing.T) {
	t.Parallel()

	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	protocPath, err := protoc.BinaryPath(root)
	if err != nil {
		t.Skipf("protoc unavailable: %v", err)
	}

	for _, tc := range []struct{ name, src string }{
		{"field number", `syntax = "proto3";
package t;
message M { int32 f = 1e2; }
`},
		{"enum number", `syntax = "proto3";
package t;
enum E { E_UNSPECIFIED = 0; E_X = 1e2; }
`},
		{"integer default", `syntax = "proto2";
package t;
message M { optional int32 f = 1 [default = 1e2]; }
`},
		{"float default", `syntax = "proto2";
package t;
message M { optional float f = 1 [default = 9e9]; }
`},
		{"double default", `syntax = "proto2";
package t;
message M { optional double f = 1 [default = 7e22]; }
`},
		{"integer default, fractional exponent", `syntax = "proto2";
package t;
message M { optional int32 f = 1 [default = 1.5e2]; }
`},
		// A `p` exponent makes a hex literal a float, and protoc agrees.
		{"hex float", `syntax = "proto2";
package t;
message M { optional double f = 1 [default = 0x1p4]; }
`},
		// `0x2E` is deliberately absent. `e` is a hex DIGIT, so mis-reading
		// it as an exponent is the other half of #191 — but protoc accepts
		// 0x2E as an integer default either way, so this test cannot see
		// the difference. It is pinned where it IS observable, in
		// fdp.TestHexDigitEIsNotAnExponent.
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			path := filepath.Join(dir, "x.proto")
			require.NoError(t, os.WriteFile(path, []byte(tc.src), 0o600))

			out, err := exec.CommandContext(t.Context(), protocPath,
				"--descriptor_set_out="+filepath.Join(dir, "out.fds"),
				"-I"+dir, path).CombinedOutput()
			protocRejects := err != nil

			opener := source.NewMap(map[string]*source.File{
				"x.proto": source.NewFile("x.proto", tc.src),
			})
			_, rep, runErr := incremental.Run(t.Context(), incremental.New(), queries.IR{
				Opener:  &source.Openers{opener, source.WKTs()},
				Session: new(ir.Session),
				Path:    "x.proto",
			})
			require.NoError(t, runErr)
			ours := false
			for _, d := range rep.Diagnostics {
				if d.Level() <= report.Error {
					ours = true
				}
			}

			assert.Equal(t, protocRejects, ours,
				"protoc %s, protocompile %s\nprotoc said: %s",
				verdict(protocRejects), verdict(ours), out)
		})
	}
}

func verdict(rejects bool) string {
	if rejects {
		return "REJECTS"
	}
	return "ACCEPTS"
}
