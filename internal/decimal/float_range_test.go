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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// IsFloat decides from the mantissa's size before building 5^k: a value
// with a billion fractional digits is not a float, and finding that out
// must not cost a 2.3-gigabit exponentiation (#210). The boundary cases
// keep their exact answers.
func TestIsFloatAnswersByMagnitude(t *testing.T) {
	t.Parallel()

	cases := []struct {
		lit  string
		want bool
	}{
		{"18.1875", true}, // 291 / 16
		{"0.1", false},    // the classic
		{"0.5", true},     // 1 / 2
		{"1e-1", false},   // 0.1 again, spelled with an exponent
		{"5e-1", true},    // 0.5 again
		{"25e-2", true},   // 0.25 = 1/4
		{"1e-1000000", false},
		{"1e-999999999", false},
		{"123456789e-999999999", false},
	}
	for _, tc := range cases {
		t.Run(tc.lit, func(t *testing.T) {
			t.Parallel()
			z, err := new(Decimal).Parse(tc.lit)
			require.NoError(t, err)
			start := time.Now()
			got := z.IsFloat()
			assert.Less(t, time.Since(start), 50*time.Millisecond, tc.lit)
			assert.Equal(t, tc.want, got, tc.lit)
		})
	}
}
