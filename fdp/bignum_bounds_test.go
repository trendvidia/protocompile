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

package fdp

import (
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// promptly fails the test if f has not returned within budget: the
// defects here are values computed exactly from a literal whose exponent
// says how long that takes — minutes to never — so a regression must
// fail, not hang the suite (#210). The budget is a minute: the values
// this guards against take far longer, and a loaded runner under -race
// stretched a 10ms compile past a 5s budget once.
func promptly(t *testing.T, budget time.Duration, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { f(); close(done) }()
	select {
	case <-done:
	case <-time.After(budget):
		t.Fatalf("did not return within %v: a value was computed from the literal's exponent", budget)
	}
}

// TestBigFloatArgIsBoundedByTheExponent pins #210 at the builder: an
// exponent big.Float cannot hold is refused in microseconds, one it can
// hold is built without materialising the value, and every literal
// within MaxNumericLiteralDigits decades still takes the exact path.
func TestBigFloatArgIsBoundedByTheExponent(t *testing.T) {
	t.Parallel()
	for _, lit := range []string{"1e999999999", "1e-999999999", "1e2000000000", "1e-646456995"} {
		t.Run("refused "+lit, func(t *testing.T) {
			t.Parallel()
			promptly(t, time.Minute, func() {
				_, ok := bigFloatArg(lit)
				assert.False(t, ok, "%s is past pxf.BigFloat's range", lit)
			})
		})
	}
	for _, lit := range []string{"1e100000000", "1e-100000000", "1e646456992", "1e5000", "1e-5000"} {
		t.Run("built "+lit, func(t *testing.T) {
			t.Parallel()
			promptly(t, time.Minute, func() {
				v, ok := bigFloatArg(lit)
				require.True(t, ok, "%s fits pxf.BigFloat", lit)
				assert.Equal(t, uint32(bigFloatPrec), v.GetPrec())
				assert.NotEmpty(t, v.GetMantissa())
			})
		})
	}
	// The guarded path agrees with the exact one where both can run: a
	// literal just inside the exact band and the same value spelled to
	// land just outside it.
	exact, ok := bigFloatArg("1.5e4096")
	require.True(t, ok)
	guarded, ok := bigFloatArg("0.15e4097")
	require.True(t, ok)
	assert.Equal(t, exact.GetMantissa(), guarded.GetMantissa(), "same value, same mantissa")
	assert.Equal(t, exact.GetExponent(), guarded.GetExponent(), "same value, same exponent")
}

// TestBigIntArgIsBoundedByDigits: a float-spelled integer past float64's
// range is still an integer and is built exactly (it used to be refused
// as "not an integer"); one past MaxNumericLiteralDigits digits is
// refused without being built.
func TestBigIntArgIsBoundedByDigits(t *testing.T) {
	t.Parallel()
	v, ok := bigIntArg("1e400")
	require.True(t, ok)
	assert.Equal(t, "1"+strings.Repeat("0", 400), new(big.Int).SetBytes(v.GetAbs()).String())
	v, ok = bigIntArg("1.5e1")
	require.True(t, ok)
	assert.Equal(t, "15", new(big.Int).SetBytes(v.GetAbs()).String())
	v, ok = bigIntArg("1e4095")
	require.True(t, ok, "4096 digits is at the cap")
	assert.Len(t, new(big.Int).SetBytes(v.GetAbs()).String(), 4096)
	promptly(t, time.Minute, func() {
		_, ok := bigIntArg("1e4096")
		assert.False(t, ok, "4097 digits is past the cap")
		_, ok = bigIntArg("1e999999999")
		assert.False(t, ok)
		_, ok = bigIntArg("1.5")
		assert.False(t, ok, "not an integer")
	})
}

// TestDecimalArgIsBoundedByScale: the scale a decoder accepts is bounded
// by MaxNumericLiteralDigits on either sign; the builder refuses beyond
// it rather than emitting a carrier no decoder reads.
func TestDecimalArgIsBoundedByScale(t *testing.T) {
	t.Parallel()
	v, ok := decimalArg("1e-4096")
	require.True(t, ok)
	assert.Equal(t, int32(4096), v.GetScale())
	v, ok = decimalArg("1e4096")
	require.True(t, ok)
	assert.Equal(t, int32(-4096), v.GetScale())
	for _, lit := range []string{"1e-4097", "1e4097", "1e999999999", "1e-999999999"} {
		_, ok := decimalArg(lit)
		assert.False(t, ok, "%s: scale past MaxNumericLiteralDigits", lit)
	}
	_, ok = decimalArg("1" + strings.Repeat("0", 4096) + ".5")
	assert.False(t, ok, "4098 significant digits")
}
