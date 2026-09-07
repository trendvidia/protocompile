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
	"context"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trendvidia/protocompile/fdp"
	"github.com/trendvidia/protocompile/incremental"
	"github.com/trendvidia/protocompile/incremental/queries"
	"github.com/trendvidia/protocompile/ir"
	"github.com/trendvidia/protocompile/report"
	"github.com/trendvidia/protocompile/source"
)

// compileUnderDeadline runs the IR compile and the fdp lowering in a
// goroutine and fails the test if either has not returned within limit —
// a hang is the failure mode under test (#210), and the default go test
// timeout is not a diagnosis. The goroutine asserts nothing: a FailNow
// off the test goroutine is undefined, so it reports and the test
// goroutine judges.
func compileUnderDeadline(t *testing.T, src string, limit time.Duration) *report.Report {
	t.Helper()
	type result struct {
		rep *report.Report
		err error
	}
	done := make(chan result, 1)
	go func() {
		f, rep, err := compileForDeadline(src)
		if err == nil {
			// A file with diagnostics still lowers — protolsp lowers
			// every open document for its source map — so the lowering
			// must return promptly too.
			_, _ = fdp.DescriptorProto(f)
		}
		done <- result{rep, err}
	}()
	select {
	case r := <-done:
		require.NoError(t, r.err)
		require.NotNil(t, r.rep)
		return r.rep
	case <-time.After(limit):
		t.Fatalf("compiling %q did not return within %v", strings.TrimSpace(src), limit)
		return nil
	}
}

// compileForDeadline is compileForAnnotationTest without the assertions,
// for use off the test goroutine.
func compileForDeadline(src string) (*ir.File, *report.Report, error) {
	opener := source.NewMap(map[string]*source.File{
		"x.proto": source.NewFile("x.proto", src),
	})
	allOpeners := &source.Openers{opener, source.WKTs()}
	results, rep, err := incremental.Run(context.Background(), incremental.New(), queries.IR{
		Opener:  allOpeners,
		Session: new(ir.Session),
		Path:    "x.proto",
	})
	if err != nil {
		return nil, nil, err
	}
	if len(results) != 1 || results[0].Value == nil {
		return nil, nil, fmt.Errorf("compile: %d results, no IR", len(results))
	}
	return results[0].Value, rep, nil
}

// TestAnnotationArgHugeLiteralReturnsPromptly pins #210 across carrier
// types: a numeric literal whose value has a billion digits is a dozen
// bytes of source, and neither the ir pass nor the lowering may do work
// proportional to the value. NumberToken.Int used to materialise the
// integer part on the way to saturating; it now answers from magnitude.
func TestAnnotationArgHugeLiteralReturnsPromptly(t *testing.T) {
	t.Parallel()

	cases := []struct {
		carrier string
		lit     string
		want    string // "" = compiles clean; otherwise a diagnostic substring
	}{
		{"int64", "1e1000000", "out of range for the annotated type `int64`"},
		{"int64", "1e999999999", "out of range for the annotated type `int64`"},
		{"uint64", "1e999999999", "out of range for the annotated type `uint64`"},
		{"double", "1e999999999", ""},
		{"float", "1e999999999", ""},
		{"pxf.BigInt", "1e999999999", "out of range for the annotated type `pxf.BigInt`"}, // a billion digits; no binder renders it
		{"pxf.Decimal", "1e1000000", "out of range for the annotated type `pxf.Decimal`"},
		{"pxf.Decimal", "1e999999999", "out of range for the annotated type `pxf.Decimal`"},
		{"double", "1e-999999999", ""},
		{"int64", "1e-999999999", "out of range for the annotated type `int64`"},
		{"pxf.BigInt", "1e-999999999", "is not an integer"},                               // used to round to zero and pass as one (#216)
		{"pxf.BigInt", "1e100000000", "out of range for the annotated type `pxf.BigInt`"}, // an integer, and past the digit cap (#216)
		{"pxf.Decimal", "1e-999999999", "out of range for the annotated type `pxf.Decimal`"},
	}
	for _, tc := range cases {
		t.Run(tc.carrier+"/"+tc.lit, func(t *testing.T) {
			t.Parallel()
			rep := compileUnderDeadline(t, `syntax = "proto3";
package pxf;

annotation default(value: any);

message BigInt { bytes abs = 1; bool negative = 2; }
message Decimal { bytes unscaled = 1; int32 scale = 2; bool negative = 3; }

message M {
  `+tc.carrier+` f = 1 @default(`+tc.lit+`);
}
`, 10*time.Second)
			if tc.want == "" {
				for _, d := range rep.Diagnostics {
					if isError(d) {
						t.Errorf("%s %s: unexpected diagnostic: %s", tc.carrier, tc.lit, d.Message())
					}
				}
				return
			}
			assert.True(t, hasErrorContaining(rep, tc.want),
				"%s %s: want %q, got: %v", tc.carrier, tc.lit, tc.want, rep.Diagnostics)
		})
	}
}

// TestAnnotationArgBigIntIsBoundedByItsText pins protocompile#216: a
// pxf.BigInt literal is judged an integer, and bounded by its digit
// count, from the text — never through float64, which rounded `1e400`
// to infinity and `1e-999999999` to zero.
func TestAnnotationArgBigIntIsBoundedByItsText(t *testing.T) {
	t.Parallel()
	cases := []struct {
		lit  string
		want string // "" = compiles clean and lowers to big_int_value
		abs  string // the exact value when it compiles
	}{
		{"1e400", "", "1" + strings.Repeat("0", 400)},
		{"1.5e1", "", "15"},
		{"1e4095", "", "1" + strings.Repeat("0", 4095)},
		{"0x1F", "", "31"},
		{"1e4096", "out of range for the annotated type `pxf.BigInt`", ""},
		{"1.5", "is not an integer", ""},
		{"1e-1", "is not an integer", ""},
		{"1.05e1", "is not an integer", ""},
	}
	for _, tc := range cases {
		t.Run(tc.lit, func(t *testing.T) {
			t.Parallel()
			rep := compileUnderDeadline(t, `syntax = "proto3";
package pxf;
annotation default(value: any);
message BigInt { bytes abs = 1; bool negative = 2; }
message M {
  pxf.BigInt f = 1 @default(`+tc.lit+`);
}
`, 2*time.Minute)
			args := lowerFirstFieldArgs(t, "pxf.BigInt", tc.lit)
			require.Len(t, args, 1)
			if tc.want == "" {
				for _, d := range rep.Diagnostics {
					if isError(d) {
						t.Errorf("%s: unexpected diagnostic: %s", tc.lit, d.Message())
					}
				}
				require.NotNil(t, args[0].GetBigIntValue(), "%s lowers to big_int_value", tc.lit)
				assert.Equal(t, tc.abs, new(big.Int).SetBytes(args[0].GetBigIntValue().GetAbs()).String())
				return
			}
			assert.True(t, hasErrorContaining(rep, tc.want), "%s: got: %v", tc.lit, rep.Diagnostics)
			if strings.Contains(tc.want, "out of range") {
				assert.Nil(t, args[0].GetValue(), "%s: a diagnosed literal lowers to no value", tc.lit)
			}
		})
	}
}
