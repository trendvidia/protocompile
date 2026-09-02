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

	"github.com/trendvidia/protocompile/internal/ext/bigx"
)

// IsInt returns whether this value is an integer.
func (z *Decimal) IsInt() bool {
	return z.IsZero() || int(z.exp) >= z.digits()
}

// Int sets x to the nearest integer to z, with a half rounded away from
// zero.
//
// The result is a magnitude: z's sign is not applied, matching the rest of
// this type, where the sign lives in flags rather than in the mantissa.
//
// If z is non-finite, returns nil and leaves x unchanged.
func (z *Decimal) Int(x *big.Int) *big.Int {
	if !z.IsFinite() {
		return nil
	}

	if x == nil {
		x = new(big.Int)
	}

	n := int(z.exp) - z.digits()
	if n < 0 {
		return z.roundToNearest(x, uint(-n))
	}

	w := x.Bits()
	if z.base2() {
		w = bigx.Scale2(w, z.get(), uint(n))
	} else {
		w = bigx.Scale10(w, z.get(), uint(n))
	}

	return x.SetBits(w)
}

// SetUint64 sets this decimal's value to x.
func (z *Decimal) SetUint64(x uint64) *Decimal {
	// Doing it this way gives us a good shot to get this slice to allocate
	// on the stack.
	xb := new(big.Int).SetBits(bigx.SetUint64(make([]big.Word, 0, 2), x))
	return z.setInt(xb, false)
}

// SetInt sets this decimal's value to x.
func (z *Decimal) SetInt(x *big.Int) *Decimal {
	return z.setInt(x, false)
}

// ReuseInt sets this decimal's value to x, consuming x's storage in the
// process.
func (z *Decimal) ReuseInt(x *big.Int) *Decimal {
	return z.setInt(x, true)
}

func (z *Decimal) setInt(x *big.Int, reuse bool) *Decimal {
	z.SetZero()

	if x.Sign() < 0 {
		z.flags |= sign
	}

	exp := x.BitLen()
	if exp > math.MaxInt32 || exp < math.MinInt32 {
		z.flags |= inf
		return z
	}

	// Because this is an integer, we can use a power of 2 exponent.
	// This simplifies the task of calculating an exponent, punting the
	// "convert to base 10" problem to later, if necessary at all.
	z.flags |= base2

	w := x.Bits()
	if !reuse || cap(w) < cap(z.get()) {
		w = append(z.get()[:0], w...)
	}

	// Knock off any trailing zeros. Because of the representation we've chosen,
	// trailing zeros are never part of the final value.
	//
	// Because we're putting this in 0.bbbbb * 2^e form, if there are trailing
	// zeros before the binary point, they are automatically filled by the
	// << implied by the 2^e.
	z.set(bigx.Shr(w, w, x.TrailingZeroBits()))
	z.exp = int32(exp)

	return z
}

// roundToNearest sets x to z's mantissa divided by base^k, rounded to the
// nearest integer with a half going away from zero.
//
// k is the number of fractional digits, so this is the path for a value
// that is not already whole. It used to return zero — for any magnitude,
// so 1000000.1 came back as 0 — which is what this replaces.
func (z *Decimal) roundToNearest(x *big.Int, k uint) *big.Int {
	// z is mantissa * base^(exp-digits) with a mantissa of exactly digits
	// digits, so its magnitude is below base^exp: a negative exponent puts
	// it under 1/base <= 1/2, and it rounds to zero.
	//
	// This is not only a shortcut. k is digits-exp, which Parse bounds only
	// by int32, so `1e-10000000` asks for a divisor of 10^10000001 — 33
	// million bits, built to divide a one-word mantissa into an answer that
	// is always 0. Measured on this path before the guard: 1e-100000 took
	// 620µs, 1e-1000000 21ms and 1e-10000000 794ms, and it grows by ~30 per
	// decade from there. Past this line exp >= 0, so k <= z.digits() and the
	// divisor is bounded by the literal the author actually wrote.
	if z.exp < 0 {
		return x.SetUint64(0)
	}

	// SetBits shares storage with the slice it is given, and z.get() is z's
	// own mantissa, so it is copied rather than aliased.
	m := new(big.Int).SetBits(append([]big.Word(nil), z.get()...))

	var d *big.Int
	if z.base2() {
		d = new(big.Int).Lsh(big.NewInt(1), k)
	} else {
		d = new(big.Int).Exp(big.NewInt(10), new(big.Int).SetUint64(uint64(k)), nil)
	}

	// x is the quotient; m and d are freshly allocated, so neither aliases
	// it, and r is distinct from both.
	r := new(big.Int)
	x.QuoRem(m, d, r)

	// The mantissa is unsigned, so "away from zero" is simply up: round when
	// the remainder is at least half the divisor, i.e. 2r >= d.
	if r.Lsh(r, 1).Cmp(d) >= 0 {
		x.Add(x, big.NewInt(1))
	}
	return x
}
