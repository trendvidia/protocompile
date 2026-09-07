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

// TestIsFloatAnswersFromSizes pins trendvidia/protocompile#210's lexer
// half: a negative exponent whose power of five is larger than the
// mantissa is decided without computing the power, so `1e-999999999`
// is answered in microseconds rather than by a two-billion-bit
// exponentiation — and the answers around the cut are unchanged.
func TestIsFloatAnswersFromSizes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		text string
		want bool
	}{
		{"18.1875", true}, // 291 / 16
		{"0.1", false},    // needs a factor of 5 the mantissa lacks
		{"0.5", true},     // 5 / 10
		{"625e-4", true},  // 5^4 / 10^4
		{"624e-4", false}, //
		{"1e-999999999", false},
		{"1e-1000000000", false},
		{"5e-1", true},
	} {
		t.Run(tc.text, func(t *testing.T) {
			t.Parallel()
			var z Decimal
			_, err := z.Parse(tc.text)
			require.NoError(t, err)
			done := make(chan bool, 1)
			go func() { done <- z.IsFloat() }()
			select {
			case got := <-done:
				assert.Equal(t, tc.want, got)
			case <-time.After(2 * time.Second):
				t.Fatal("IsFloat computed the power of five instead of comparing sizes")
			}
		})
	}
}
