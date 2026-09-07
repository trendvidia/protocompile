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

package protocompile_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trendvidia/protocompile"
)

// TestArbitraryPrecisionLiteralsCompilePromptly is #210 end to end:
// `@default(1e999999999)` on a pxf.BigFloat field did not return, because
// the lowering fell back to the literal's exact integer value after the
// carrier builder gave up. Every carrier now answers from the literal's
// text; a compile that hangs here is the regression.
func TestArbitraryPrecisionLiteralsCompilePromptly(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		msg, lit string
		ok       bool
	}{
		{"BigFloat", "1e999999999", false},
		{"BigFloat", "1e-999999999", false},
		{"BigFloat", "1e100000000", true},
		{"Decimal", "1e999999999", false},
		{"BigInt", "1e999999999", false},
		{"BigInt", "1e400", true},
	} {
		t.Run("pxf."+tc.msg+" "+tc.lit, func(t *testing.T) {
			t.Parallel()
			src := "syntax = \"proto3\";\npackage pxf;\nannotation default(value: any);\nmessage " + tc.msg +
				" { bytes abs = 1; bool negative = 2; }\nmessage M { " + tc.msg + " f = 1 @default(" + tc.lit + "); }\n"
			comp := protocompile.Compiler{Resolver: protocompile.WithStandardImports(&protocompile.SourceResolver{
				Accessor: protocompile.SourceAccessorFromMap(map[string]string{"z.proto": src}),
			})}
			done := make(chan error, 1)
			start := time.Now()
			go func() { _, err := comp.Compile(context.Background(), "z.proto"); done <- err }()
			select {
			case err := <-done:
				if tc.ok {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
					assert.Contains(t, err.Error(), "out of range for the annotated type `pxf."+tc.msg+"`")
				}
			// A hang here is minutes to never, so the budget is large: the
			// first cut said 5s, and on a loaded runner under -race every
			// case took 5.01s — a compile that takes 10ms unloaded.
			case <-time.After(2 * time.Minute):
				t.Fatalf("compile did not return within 2m (elapsed %v)", time.Since(start))
			}
		})
	}
}
