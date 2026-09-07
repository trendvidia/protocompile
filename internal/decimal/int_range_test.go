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
	"math"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// IsUint64Possible answers from magnitude alone, so it is exact at the
// digit-count boundary and never builds the value: a literal with a
// billion-digit integer part answers in nanoseconds, where Int would
// spend hours (#210).
func TestIsUint64PossibleByMagnitude(t *testing.T) {
	t.Parallel()

	cases := []struct {
		lit      string
		possible bool
	}{
		{"0", true},
		{"1", true},
		{"0.5", true},
		{"1e-999999999", true}, // below one: nothing to fit, Int rounds it away cheaply
		{"18446744073709551615", true},
		{"18446744073709551616", true}, // 20 digits: possible by magnitude; Int says no
		{"1e19", true},
		{"1e20", false},
		{"123456789012345678901", false},
		{"1e400", false},
		{"1e1000000", false},
		{"1e999999999", false},
		{"0x1_0000_0000_0000_0000", false}, // 2^64, 65 bits
		{"0xFFFF_FFFF_FFFF_FFFF", true},
	}
	for _, tc := range cases {
		t.Run(tc.lit, func(t *testing.T) {
			t.Parallel()
			z, err := new(Decimal).Parse(tc.lit)
			require.NoError(t, err)
			start := time.Now()
			got := z.IsUint64Possible()
			assert.Less(t, time.Since(start), 10*time.Millisecond)
			assert.Equal(t, tc.possible, got, tc.lit)
			if got {
				// Cheap by construction: at most 20 decimal digits of scaling.
				start = time.Now()
				v := z.Int(nil)
				assert.Less(t, time.Since(start), 100*time.Millisecond)
				require.NotNil(t, v)
			}
		})
	}

	z, err := new(Decimal).Parse("18446744073709551615")
	require.NoError(t, err)
	assert.Equal(t, uint64(math.MaxUint64), z.Int(nil).Uint64())
	assert.Zero(t, new(big.Int).SetUint64(math.MaxUint64).Cmp(z.Int(nil)))
}
