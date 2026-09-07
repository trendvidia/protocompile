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

package bigx

import (
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The triple rebuilds the value big.ParseFloat gives at the same
// precision — the same call protowire-go's decoder makes on the same text.
//
// Failure messages render with the binary 'p' format: a decimal rendering
// of a value like 1e646456992 costs time proportional to the exponent
// (protowire#281), and assertion arguments are evaluated eagerly.
func TestBigFloatLiteralMatchesParseFloat(t *testing.T) {
	t.Parallel()

	for _, lit := range []string{
		"0", "0.0", "0e5", "1.5", "-1.5", "0.5", ".5", "18446744073709551615",
		"1.2345678901234567890e19", "1e100", "1e400", "3.14159e-400",
		"1_000.5", "0x1F", "-0b101", "0o17",
		"1e4096", "1e-4096", "1e100000", "1e1000000", "1e646456992",
	} {
		t.Run(lit, func(t *testing.T) {
			t.Parallel()
			mant, exp, neg, err := BigFloatLiteral(lit, 256)
			require.NoError(t, err)

			got := new(big.Float).SetPrec(256).SetMantExp(new(big.Float).SetPrec(256).SetInt(mant), int(exp))
			if neg {
				got.Neg(got)
			}
			var want *big.Float
			if i, ok := new(big.Int).SetString(strings.ReplaceAll(lit, "_", ""), 0); ok && strings.ContainsAny(lit, "xob") {
				want = new(big.Float).SetPrec(256).SetInt(i)
			} else {
				var perr error
				want, _, perr = big.ParseFloat(strings.ReplaceAll(lit, "_", ""), 10, 256, big.ToNearestEven)
				require.NoError(t, perr)
			}
			assert.Zero(t, want.Cmp(got), "%s: want %s, got %s", lit, want.Text('p', 0), got.Text('p', 0))
		})
	}
}

// Conversion cost must not scale with the exponent (#210): the largest
// magnitudes the wire holds, and the ones just beyond it, all return in
// well under a second. Sequential, so the bound is not disturbed by
// parallel siblings.
func TestBigFloatLiteralIsFastAtHugeExponents(t *testing.T) {
	for _, lit := range []string{"1e1000000", "1e646456992", "1e646456993", "1e999999999", "1e-999999999"} {
		start := time.Now()
		_, _, _, _ = BigFloatLiteral(lit, 256)
		assert.Less(t, time.Since(start), time.Second, lit)
	}
}

// Beyond the wire: the last representable magnitude is 1e646456992; one
// more overflows, and the mirror underflows the int32 exponent before
// big.Float underflows to zero.
func TestBigFloatLiteralRange(t *testing.T) {
	t.Parallel()

	for _, lit := range []string{
		"1e646456993", "9.9e646456992", "1e999999999", "-1e999999999",
		"1e-646456992", "1e-646456993", "1e-999999999", "-1e-999999999",
	} {
		t.Run(lit, func(t *testing.T) {
			t.Parallel()
			_, _, _, err := BigFloatLiteral(lit, 256)
			assert.ErrorIs(t, err, ErrRange, "%s: want ErrRange, got %v", lit, err)
		})
	}
	_, exp, _, err := BigFloatLiteral("1e646456992", 256)
	require.NoError(t, err)
	assert.Equal(t, int32(2147483388), exp, "the largest magnitude still leaves the wire exponent inside int32")
}

func TestBigFloatLiteralSyntax(t *testing.T) {
	t.Parallel()
	for _, lit := range []string{"", "e5", "1e", "0x", "abc", "1.2.3"} {
		_, _, _, err := BigFloatLiteral(lit, 256)
		require.Error(t, err, lit)
		assert.NotErrorIs(t, err, ErrRange, "%s is malformed, not out of range", lit)
	}
}

func TestDecimalScale(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		lit   string
		scale int64
		ok    bool
	}{
		{"1", 0, true}, {"1.50", 2, true}, {"1.5e2", -1, true}, {"-0.001", 3, true},
		{"1e4096", -4096, true}, {"1e-4096", 4096, true}, {"1.5e-4095", 4096, true},
		{"1e999999999", -999999999, true}, {"1e-999999999", 999999999, true},
		{"1_000.25", 2, true}, {"0x1F", 0, true},
		{"1e", 0, false}, {"e5", 0, false}, {".", 0, false}, {"1e99999999999999999999", 0, false},
	} {
		scale, ok := DecimalScale(tc.lit)
		assert.Equal(t, tc.ok, ok, tc.lit)
		if ok {
			assert.Equal(t, tc.scale, scale, tc.lit)
		}
	}
}

func TestIntegerShape(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		lit      string
		digits   int64
		integral bool
		ok       bool
	}{
		{"42", 2, true, true}, {"1.50", 1, false, true}, {"1.5e1", 2, true, true}, {"1.50e1", 2, true, true},
		{"1e400", 401, true, true}, {"1e-400", 0, false, true}, {"0.0e5", 0, true, true}, {"007", 1, true, true},
		{"1_000", 4, true, true}, {"0x10", 2, true, true}, {"-0b0", 0, true, true},
		{"1e999999999", 1000000000, true, true}, {"1e-999999999", 0, false, true},
		{"1e", 0, false, false}, {"abc", 0, false, false}, {"1e99999999999999999999", 0, false, false},
	} {
		digits, integral, ok := IntegerShape(tc.lit)
		assert.Equal(t, tc.ok, ok, tc.lit)
		if ok {
			assert.Equal(t, tc.digits, digits, tc.lit)
			assert.Equal(t, tc.integral, integral, tc.lit)
		}
	}
}
