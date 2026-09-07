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
	"errors"
	"math"
	"math/big"
	"strconv"
	"strings"
)

// ErrRange reports a numeric literal whose value the target carrier cannot
// represent: for pxf.BigFloat, one whose binary exponent falls outside the
// wire's int32 once the mantissa is normalised, or that overflows or
// underflows big.Float itself. A carrier is never an infinity, a zero
// standing in for a tiny value, or a wrapped exponent (protowire
// HARDENING.md, "Arbitrary-precision magnitudes").
var ErrRange = errors.New("value out of range for the carrier")

// BigFloatLiteral converts a PXF numeric literal to the pxf.BigFloat wire
// triple: the mantissa as an integer of at most prec bits, the binary
// exponent of that integer, and the sign — value = mant × 2^exp.
//
// The literal is converted in floating point at prec bits, never through
// an exact rational: a decimal exponent of n would otherwise materialise
// 10^n as an integer, which at n = 10⁹ is hundreds of megabytes and hours
// (#210). big.Float.Parse costs O(log n) multiplications at prec bits, and
// it is what protowire-go's own PXF decoder uses for the same text, so the
// compiler's carrier and the runtime's parse agree bit for bit; for every
// literal the exact path could finish, the two are identical.
//
// The literal's syntax is the PXF numeric grammar — an optional sign,
// decimal digits with an optional fraction and exponent, or a 0x / 0o / 0b
// based integer — with `_` separators allowed anywhere. A syntax error is
// reported as-is; a value the carrier cannot hold is [ErrRange].
func BigFloatLiteral(text string, prec uint) (mant *big.Int, exp int32, neg bool, err error) {
	t := strings.ReplaceAll(text, "_", "")
	lower := strings.ToLower(t)
	f := new(big.Float).SetPrec(prec)
	switch {
	case strings.HasPrefix(lower, "0x"), strings.HasPrefix(lower, "0o"), strings.HasPrefix(lower, "0b"),
		strings.HasPrefix(lower, "-0x"), strings.HasPrefix(lower, "-0o"), strings.HasPrefix(lower, "-0b"):
		i, ok := new(big.Int).SetString(t, 0)
		if !ok {
			return nil, 0, false, errors.New("malformed based literal")
		}
		f.SetInt(i)
	default:
		if _, _, err := f.Parse(t, 10); err != nil {
			return nil, 0, false, err
		}
	}
	if f.IsInf() {
		return nil, 0, false, ErrRange
	}
	if f.Sign() == 0 && strings.ContainsAny(strings.TrimLeft(strings.SplitN(lower, "e", 2)[0], "-0."), "123456789") {
		// Underflow: the literal has a significant digit, the value has
		// none. A zero literal (0, 0.0, 0e5) is a genuine zero.
		return nil, 0, false, ErrRange
	}

	m := new(big.Float).SetPrec(prec)
	e := f.MantExp(m)
	m.SetMantExp(m, int(prec))
	mant, _ = m.Int(nil)
	if mant.Sign() < 0 {
		mant.Neg(mant)
	}
	wire := int64(e) - int64(prec)
	if wire < math.MinInt32 || wire > math.MaxInt32 {
		return nil, 0, false, ErrRange
	}
	return mant, int32(wire), f.Signbit(), nil
}

// MaxNumericLiteralDigits is protowire's hardening limit on the digit
// count of a numeric literal and on the magnitude of pxf.Decimal.scale on
// the wire (protowire HARDENING.md, "Mandatory limits"): a decoder that
// materialises a Decimal computes 10^scale, and MUST reject a scale beyond
// this before doing so. The compiler diagnoses the same bound at the
// source, so no schema emits a carrier the runtime will refuse.
const MaxNumericLiteralDigits = 4096

// DecimalScale computes the pxf.Decimal scale a numeric literal denotes —
// its fractional digit count less its exponent, so `1.50` is 2 and `1.5e2`
// is -1 — from the text alone, without materialising anything. A based
// literal has scale 0. ok is false for a malformed literal or an exponent
// that does not fit int64.
func DecimalScale(text string) (scale int64, ok bool) {
	t := strings.ReplaceAll(text, "_", "")
	lower := strings.ToLower(strings.TrimPrefix(t, "-"))
	if strings.HasPrefix(lower, "0x") || strings.HasPrefix(lower, "0o") || strings.HasPrefix(lower, "0b") {
		return 0, true
	}
	mantissa, exponent := t, int64(0)
	if i := strings.IndexAny(t, "eE"); i != -1 {
		e, err := strconv.ParseInt(t[i+1:], 10, 64)
		if err != nil {
			return 0, false
		}
		mantissa, exponent = t[:i], e
	}
	var frac int64
	if i := strings.IndexByte(mantissa, '.'); i != -1 {
		frac = int64(len(mantissa) - i - 1)
	}
	if strings.TrimLeft(mantissa, "-.") == "" {
		return 0, false
	}
	return frac - exponent, true
}
