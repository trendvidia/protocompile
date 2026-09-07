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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trendvidia/protocompile/ir"
)

func TestParseNumeralShape(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		text   string
		digits string
		scale  int64
		isInt  bool
		intLen int64
	}{
		{"42", "42", 0, true, 2},
		{"1.50", "150", 2, false, 1},
		{"1.5e1", "15", 0, true, 2},
		{"1.5e2", "15", -1, true, 3},
		{"1e400", "1", -400, true, 401},
		{"1e-400", "1", 400, false, 0},
		{"0.0e5", "", -4, true, 0},
		{"1_000", "1000", 0, true, 4},
		{"0x10", "16", 0, true, 2},
		{"007", "7", 0, true, 1},
		{"1e999999999", "1", -999999999, true, 1000000000},
	} {
		s, ok := ir.ParseNumeralShape(tc.text)
		require.True(t, ok, tc.text)
		assert.Equal(t, tc.digits, s.Digits, tc.text)
		assert.Equal(t, tc.scale, s.Scale, tc.text)
		assert.Equal(t, tc.isInt, s.IsInteger(), tc.text)
		assert.Equal(t, tc.intLen, s.IntegerDigits(), tc.text)
	}
	_, ok := ir.ParseNumeralShape("1e99999999999999999999")
	assert.False(t, ok, "an exponent past int64 is not a shape")
	_, ok = ir.ParseNumeralShape("abc")
	assert.False(t, ok)
}

// TestArbitraryPrecisionCarriersAreBoundedByText pins #210 at the
// diagnostic: each carrier's bound is read off the literal and reported
// as out of range, in milliseconds, and the values that fit compile.
func TestArbitraryPrecisionCarriersAreBoundedByText(t *testing.T) {
	t.Parallel()

	compile := func(t *testing.T, msg, lit string) []string {
		t.Helper()
		var msgs []string
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, rep := compileForAnnotationTest(t, `syntax = "proto3";
package pxf;
annotation default(value: any);
message `+msg+` { bytes abs = 1; bool negative = 2; }
message M { `+msg+` f = 1 @default(`+lit+`); }
`)
			for i := range rep.Diagnostics {
				if isError(rep.Diagnostics[i]) {
					msgs = append(msgs, rep.Diagnostics[i].Message())
				}
			}
		}()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatalf("pxf.%s @default(%s) did not compile within 3s", msg, lit)
		}
		return msgs
	}

	for _, tc := range []struct{ msg, lit, want string }{
		{"BigFloat", "1e999999999", "out of range for the annotated type `pxf.BigFloat`"},
		{"BigFloat", "1e-999999999", "out of range for the annotated type `pxf.BigFloat`"},
		{"BigFloat", "1e99999999999999999999", "out of range for the annotated type `pxf.BigFloat`"},
		{"Decimal", "1e999999999", "out of range for the annotated type `pxf.Decimal`"},
		{"Decimal", "1e-4097", "out of range for the annotated type `pxf.Decimal`"},
		{"BigInt", "1e999999999", "out of range for the annotated type `pxf.BigInt`"},
		{"BigInt", "1e4096", "out of range for the annotated type `pxf.BigInt`"},
		{"BigInt", "1.5", "is not an integer, but the annotated type `pxf.BigInt` is"},
		{"BigInt", "1e-400", "is not an integer"},
	} {
		t.Run("rejected pxf."+tc.msg+" "+tc.lit, func(t *testing.T) {
			msgs := compile(t, tc.msg, tc.lit)
			require.NotEmpty(t, msgs, "must be diagnosed")
			assert.Contains(t, strings.Join(msgs, "\n"), tc.want)
		})
	}
	for _, tc := range [][2]string{
		{"BigFloat", "1e100000000"}, {"BigFloat", "1e-100000000"}, {"BigFloat", "1e646456992"}, {"BigFloat", "1e5000"},
		{"Decimal", "1e4096"}, {"Decimal", "1e-4096"},
		{"BigInt", "1e400"}, {"BigInt", "1e4095"}, {"BigInt", "1.5e1"},
	} {
		t.Run("accepted pxf."+tc[0]+" "+tc[1], func(t *testing.T) {
			assert.Empty(t, compile(t, tc[0], tc[1]))
		})
	}
}
