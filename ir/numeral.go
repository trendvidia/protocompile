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

package ir

import (
	"math"
	"math/big"
	"strconv"
	"strings"
)

// MaxNumericLiteralDigits is protowire's cap on the digit count of a
// numeric literal (draft -01 § Mandatory Limits, 4096). It is a decoder
// limit, and it binds a compiler through what the compiler emits: a
// pxf.BigInt or pxf.Decimal carrier is rendered back to a PXF literal by
// every binder that applies a default, and a literal past the cap is
// rejected there (trendvidia/protowire#278; protowire-go#102). Emitting
// one would hand a consumer a schema no decoder accepts.
const MaxNumericLiteralDigits = 4096

// BigFloatPrec is the mantissa precision of a pxf.BigFloat carrier.
//
// A source literal is decimal, so it generally has no exact binary
// representation at any precision; something must be chosen. 256 bits is
// well above float64's 53 and above what any decimal literal a human
// writes needs, and it is fixed rather than derived so that the same
// literal always produces the same bytes.
const BigFloatPrec = 256

// NumeralShape is a numeric literal read from its text as
// digits × 10^(-scale), without computing the value: the digit string
// and the scale are what the arbitrary-precision carriers are bounded by
// (trendvidia/protocompile#210), and both come from the literal's length
// and its exponent, never from its magnitude.
//
// Digits has no sign (a leading `-` is a prefix expression, not part of
// the literal), no leading zeros, and is empty for zero. A based literal
// (0x, 0o, 0b) is converted to decimal digits first — its digit count is
// bounded by its own length, so that costs nothing proportional to the
// value.
type NumeralShape struct {
	Digits string
	Scale  int64
}

// ParseNumeralShape reads a literal's text. ok is false for text that is
// not a numeral, and for an exponent that does not fit int64 — which is
// out of range for every carrier before any other question is asked.
func ParseNumeralShape(text string) (shape NumeralShape, ok bool) {
	t := strings.ReplaceAll(text, "_", "")
	lower := strings.ToLower(t)
	if strings.HasPrefix(lower, "0x") || strings.HasPrefix(lower, "0o") ||
		strings.HasPrefix(lower, "0b") {
		i, ok := new(big.Int).SetString(t, 0)
		if !ok {
			return NumeralShape{}, false
		}
		s := strings.TrimLeft(i.String(), "0")
		return NumeralShape{Digits: s}, true
	}

	mantissa, exponent := t, int64(0)
	if i := strings.IndexAny(t, "eE"); i != -1 {
		e, err := strconv.ParseInt(t[i+1:], 10, 64)
		if err != nil {
			return NumeralShape{}, false
		}
		mantissa, exponent = t[:i], e
	}
	var frac int64
	if i := strings.IndexByte(mantissa, '.'); i != -1 {
		frac = int64(len(mantissa) - i - 1)
		mantissa = mantissa[:i] + mantissa[i+1:]
	}
	if mantissa == "" {
		return NumeralShape{}, false
	}
	for i := 0; i < len(mantissa); i++ {
		if mantissa[i] < '0' || mantissa[i] > '9' {
			return NumeralShape{}, false
		}
	}
	// Trailing fractional zeros are precision the author wrote, and
	// pxf.Decimal keeps them; the shape keeps them too, so the scale is
	// the text's. Leading zeros carry no value and go.
	digits := strings.TrimLeft(mantissa, "0")
	return NumeralShape{Digits: digits, Scale: frac - exponent}, true
}

// IsZero reports whether the literal's value is zero.
func (s NumeralShape) IsZero() bool { return s.Digits == "" }

// IsInteger reports whether the value has no fractional part: the scale
// is at most zero, or every digit the scale would put after the point is
// a zero.
func (s NumeralShape) IsInteger() bool {
	if s.Scale <= 0 || s.IsZero() {
		return true
	}
	if s.Scale >= int64(len(s.Digits)) {
		return strings.Trim(s.Digits, "0") == ""
	}
	return strings.Trim(s.Digits[len(s.Digits)-int(s.Scale):], "0") == ""
}

// IntegerDigits is the digit count of the value's integer part: what a
// pxf.BigInt carrier would be rendered as. Zero for a value below one.
func (s NumeralShape) IntegerDigits() int64 {
	n := int64(len(s.Digits)) - s.Scale
	if n < 0 || s.IsZero() {
		return 0
	}
	return n
}

// BigFloatFits reports whether the value can be carried by pxf.BigFloat
// at BigFloatPrec: finite in big.Float, and with a wire exponent
// (binary exponent minus precision) that fits int32. It is computed with
// big.Float.Parse, whose cost in the exponent is logarithmic, so a
// literal like 1e999999999 is answered in microseconds rather than by
// materialising the value (trendvidia/protocompile#210).
func (s NumeralShape) BigFloatFits(text string) bool {
	f, _, err := new(big.Float).SetPrec(BigFloatPrec+64).Parse(strings.ReplaceAll(text, "_", ""), 0)
	if err != nil || f.IsInf() {
		return false
	}
	if f.Sign() == 0 {
		// Zero is carried as zero; a non-zero literal that came back as
		// zero underflowed big.Float's range.
		return s.IsZero()
	}
	e := int64(f.MantExp(nil)) - BigFloatPrec
	return e >= math.MinInt32 && e <= math.MaxInt32
}
