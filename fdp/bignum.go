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
	"math"
	"math/big"
	"strconv"
	"strings"

	pxf "github.com/trendvidia/protocompile/gen/pxf"
	"github.com/trendvidia/protocompile/ir"
)

// Encoding for the arbitrary-precision AnnotationArg members
// (protowire#263). These shapes are not free choices: protowire-go
// already decodes them, so the field meanings below match
// `encoding/pb/pb.go`'s marshalBigInt / ratToDecimal / marshalBigFloat.
//
// The value is rebuilt from the literal's SOURCE TEXT rather than from
// token.NumberToken's parsed form: internal/decimal keeps its big.Word
// access unexported, and the text is what the author actually wrote —
// which is the whole point of these three types.

// bigRatFromText parses a numeric literal's text exactly.
//
// big.Rat handles decimal and exponent forms; the based prefixes go
// through big.Int, which big.Rat's own parser does not accept.
func bigRatFromText(text string) (*big.Rat, bool) {
	t := strings.ReplaceAll(text, "_", "")
	lower := strings.ToLower(t)
	if strings.HasPrefix(lower, "0x") || strings.HasPrefix(lower, "0o") ||
		strings.HasPrefix(lower, "0b") {
		i, ok := new(big.Int).SetString(t, 0)
		if !ok {
			return nil, false
		}
		return new(big.Rat).SetInt(i), true
	}
	r, ok := new(big.Rat).SetString(t)
	return r, ok
}

// bigIntArg builds pxf.BigInt. Reports false when the literal is not an
// integer or has more than ir.MaxNumericLiteralDigits digits, both of
// which the ir pass diagnoses before lowering runs; the guard is here so
// a file that does not compile still lowers to something — and so that
// lowering never computes a value the bound was meant to keep out
// (#210). The shape is read from the text; only a bounded value is
// built.
func bigIntArg(text string) (*pxf.BigInt, bool) {
	shape, ok := ir.ParseNumeralShape(text)
	if !ok || !shape.IsInteger() || shape.IntegerDigits() > ir.MaxNumericLiteralDigits {
		return nil, false
	}
	i, ok := integerOf(shape)
	if !ok {
		return nil, false
	}
	return &pxf.BigInt{
		Abs:      i.Bytes(),
		Negative: false, // a literal carries no sign; the prefix is folded in by the caller
	}, true
}

// integerOf is the integer value of an integral shape: the digits with
// the scale applied, which after IsInteger is a shift by at most
// ir.MaxNumericLiteralDigits decimal places.
func integerOf(shape ir.NumeralShape) (*big.Int, bool) {
	if shape.IsZero() {
		return new(big.Int), true
	}
	digits := shape.Digits
	scale := shape.Scale
	if scale > 0 {
		// Trailing zeros after the point: drop them.
		digits = digits[:len(digits)-int(scale)]
		scale = 0
	}
	i, ok := new(big.Int).SetString(digits, 10)
	if !ok {
		return nil, false
	}
	if scale < 0 {
		i.Mul(i, new(big.Int).Exp(big.NewInt(10), big.NewInt(-scale), nil))
	}
	return i, true
}

// decimalArg builds pxf.Decimal, where value = unscaled x 10^(-scale).
//
// Scale comes from the TEXT, not from the value. bignum.proto states that
// the decimal literal preserves its exact scale -- `"1.00"` has scale 2 --
// and a big.Rat cannot carry that: it normalises 1.50 to 3/2, and the
// trailing zero, which is the author's statement of precision, is gone.
//
// scale = (digits after the point) - (exponent), so `1.50` is
// unscaled 150 scale 2, and `1.5e2` is unscaled 15 scale -1. Both denote
// the same value as their text; only the first claims two decimal places.
func decimalArg(text string) (*pxf.Decimal, bool) {
	t := strings.ReplaceAll(text, "_", "")
	lower := strings.ToLower(t)

	// A based literal has no fractional part and no exponent.
	if strings.HasPrefix(lower, "0x") || strings.HasPrefix(lower, "0o") ||
		strings.HasPrefix(lower, "0b") {
		i, ok := new(big.Int).SetString(t, 0)
		if !ok {
			return nil, false
		}
		return &pxf.Decimal{
			Unscaled: new(big.Int).Abs(i).Bytes(),
			Negative: i.Sign() < 0,
		}, true
	}

	mantissa, exponent := t, int32(0)
	if i := strings.IndexAny(t, "eE"); i != -1 {
		e, err := strconv.ParseInt(t[i+1:], 10, 32)
		if err != nil {
			return nil, false
		}
		mantissa, exponent = t[:i], int32(e)
	}

	var frac int32
	if i := strings.IndexByte(mantissa, '.'); i != -1 {
		frac = int32(len(mantissa) - i - 1)
		mantissa = mantissa[:i] + mantissa[i+1:]
	}
	if mantissa == "" || mantissa == "-" {
		return nil, false
	}
	// Both bounds are the ir pass's (#210); repeated here because lowering
	// runs over a file that does not compile, and must not build what the
	// bound keeps out.
	if scale := int64(frac) - int64(exponent); scale > ir.MaxNumericLiteralDigits || scale < -ir.MaxNumericLiteralDigits ||
		len(strings.TrimLeft(mantissa, "0")) > ir.MaxNumericLiteralDigits {
		return nil, false
	}

	unscaled, ok := new(big.Int).SetString(mantissa, 10)
	if !ok {
		return nil, false
	}
	return &pxf.Decimal{
		Unscaled: new(big.Int).Abs(unscaled).Bytes(),
		Scale:    frac - exponent,
		Negative: unscaled.Sign() < 0,
	}, true
}

// bigFloatPrec is ir.BigFloatPrec, the one place the choice is stated.
const bigFloatPrec = ir.BigFloatPrec

// bigFloatArg builds pxf.BigFloat, matching protowire-go's
// marshalBigFloat: mantissa is the value scaled to an integer at `prec`
// bits, and exponent is the binary exponent adjusted by that scaling.
//
// Two paths, by the literal's decimal exponent. Within
// ir.MaxNumericLiteralDigits decades the value is built exactly as a
// big.Rat and rounded once, as it always was — every literal a person
// writes is here, and its bytes do not change. Beyond, an exact value is
// a ten-to-the-millions integer and building it is what did not return
// on `1e999999999` (#210); big.Float.Parse at 64 guard bits then one
// rounding to bigFloatPrec costs logarithmic time in the exponent, and
// agrees with the exact path except on a value within 2^-64 of a
// rounding boundary. Reports false for a value big.Float cannot hold or
// whose wire exponent does not fit int32; the ir pass has diagnosed
// those before lowering runs.
func bigFloatArg(text string) (*pxf.BigFloat, bool) {
	shape, ok := ir.ParseNumeralShape(text)
	if !ok || int64(len(shape.Digits)) > ir.MaxNumericLiteralDigits || !shape.BigFloatFits(text) {
		return nil, false
	}
	bf := new(big.Float).SetPrec(bigFloatPrec)
	if shape.Scale >= -ir.MaxNumericLiteralDigits && shape.Scale <= ir.MaxNumericLiteralDigits {
		r, ok := bigRatFromText(text)
		if !ok {
			return nil, false
		}
		bf.SetRat(r)
	} else {
		guarded, _, err := new(big.Float).SetPrec(bigFloatPrec+64).Parse(strings.ReplaceAll(text, "_", ""), 0)
		if err != nil {
			return nil, false
		}
		bf.Set(guarded)
	}

	mant := new(big.Float).SetPrec(bigFloatPrec)
	exp := bf.MantExp(mant)
	mant.SetMantExp(mant, bigFloatPrec)
	mantInt, _ := mant.Int(nil)
	if mantInt.Sign() < 0 {
		mantInt.Neg(mantInt)
	}
	adj := int64(exp) - int64(bigFloatPrec)
	if adj < math.MinInt32 || adj > math.MaxInt32 {
		return nil, false
	}

	return &pxf.BigFloat{
		Mantissa: mantInt.Bytes(),
		Exponent: int32(adj),
		Prec:     uint32(bigFloatPrec),
		Negative: bf.Signbit(),
	}, true
}
