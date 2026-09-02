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

package decimal

import (
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIntRoundsToNearest pins what Int does with a value that is not
// already whole.
//
// It used to return zero for every such value, at any magnitude —
// 1000000.1 came back as 0 — while its doc said "the nearest integer to z".
// The magnitudes below are deliberately spread, because a rounding bug and
// a return-zero bug look identical when every fixture is smaller than one
// (#167).
func TestIntRoundsToNearest(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		in   string
		want int64
	}{
		{"1.1", 1},
		{"1.4", 1},
		{"1.6", 2},
		{"2.9", 3},
		{"0.4", 0},
		{"0.6", 1},

		// Halves go away from zero. Int returns a magnitude, so that is
		// simply up.
		{"0.5", 1},
		{"1.5", 2},
		{"2.5", 3},

		// The sign is carried in flags, not the mantissa, so Int reports
		// the magnitude and these match their positive counterparts.
		{"-1.6", 2},
		{"-2.5", 3},

		// Large enough that returning zero would be unmistakable.
		{"1000000.1", 1000000},
		{"123456789.5", 123456790},

		// Already whole: unchanged, and not routed through rounding.
		{"3", 3},
		{"1e3", 1000},
		{"0", 0},

		// Base 2 takes the other divisor. 0x1.8p1 is 3; 0x1.4p1 is 2.5.
		{"0x1.8p1", 3},
		{"0x1.4p1", 3},
	} {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			var d Decimal
			_, err := d.Parse(tc.in)
			require.NoError(t, err)
			assert.Equal(t, tc.want, d.Int(new(big.Int)).Int64())
		})
	}
}

// TestIntBelowHalfIsZeroWithoutBuildingTheDivisor covers the state every
// other fixture here misses: a fraction whose digit count is small. `k` is
// digits-exp, and Parse bounds the exponent only by int32, so a literal as
// short as `1e-100000000` asks roundToNearest for a 332-million-bit
// divisor — to divide a one-word mantissa into an answer that is always 0,
// because a negative exponent puts the value below 1/2.
//
// The value alone does not pin this: returning zero is also what the bug in
// #167 did. The deadline is the assertion. Without the short-circuit this
// takes tens of seconds and hundreds of megabytes; with it, microseconds.
func TestIntBelowHalfIsZeroWithoutBuildingTheDivisor(t *testing.T) {
	t.Parallel()

	for _, in := range []string{"1e-100000000", "0x1p-100000000", "1.5e-2000000000"} {
		t.Run(in, func(t *testing.T) {
			t.Parallel()

			done := make(chan int64, 1)
			go func() {
				var d Decimal
				if _, err := d.Parse(in); err != nil {
					close(done)
					return
				}
				done <- d.Int(new(big.Int)).Int64()
			}()

			select {
			case got, ok := <-done:
				require.True(t, ok, "parse failed")
				assert.Equal(t, int64(0), got)
			case <-time.After(5 * time.Second):
				t.Fatal("Int built the divisor instead of short-circuiting")
			}
		})
	}
}

// TestIntRoundsMultiWordMantissa covers the other state the table above
// misses: every literal there has a mantissa that fits in a single
// big.Word, so the copy, the division and the carry are never exercised
// across word boundaries.
func TestIntRoundsMultiWordMantissa(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ in, want string }{
		{"123456789012345678901234567890.4", "123456789012345678901234567890"},
		{"123456789012345678901234567890.5", "123456789012345678901234567891"},
		// A carry that propagates the whole way: .5 rounds up through
		// every nine.
		{"99999999999999999999999999999.5", "100000000000000000000000000000"},
	} {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			var d Decimal
			_, err := d.Parse(tc.in)
			require.NoError(t, err)
			assert.Equal(t, tc.want, d.Int(new(big.Int)).String())
		})
	}
}

// TestIntNilDestinationStillAllocates keeps the documented nil-x contract
// working on the rounding path, which allocates its own result.
func TestIntNilDestinationStillAllocates(t *testing.T) {
	t.Parallel()

	var d Decimal
	_, err := d.Parse("2.9")
	require.NoError(t, err)
	got := d.Int(nil)
	require.NotNil(t, got)
	assert.Equal(t, int64(3), got.Int64())
}

// TestIntDoesNotMutateTheReceiver guards the aliasing risk in the rounding
// path: big.Int.SetBits shares storage with the slice it is handed, and the
// mantissa it needs belongs to the Decimal.
func TestIntDoesNotMutateTheReceiver(t *testing.T) {
	t.Parallel()

	var d Decimal
	_, err := d.Parse("123456789.5")
	require.NoError(t, err)

	before, _ := d.Float64()
	assert.Equal(t, int64(123456790), d.Int(new(big.Int)).Int64())
	assert.Equal(t, int64(123456790), d.Int(new(big.Int)).Int64(), "second call must agree")

	after, _ := d.Float64()
	assert.InDelta(t, before, after, 0, "Int must not disturb the value it read")
}
