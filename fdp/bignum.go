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
	"strconv"
	"strings"

	pxf "github.com/trendvidia/protocompile/gen/pxf"
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
// integer, which the ir pass diagnoses before lowering runs; the guard is
// here so a file that does not compile still lowers to something.
func bigIntArg(text string) (*pxf.BigInt, bool) {
	r, ok := bigRatFromText(text)
	if !ok || !r.IsInt() {
		return nil, false
	}
	i := r.Num()
	return &pxf.BigInt{
		Abs:      new(big.Int).Abs(i).Bytes(),
		Negative: i.Sign() < 0,
	}, true
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

// bigFloatPrec is the mantissa precision used for pxf.BigFloat.
//
// A source literal is decimal, so it generally has no exact binary
// representation at any precision; something must be chosen. 256 bits is
// well above float64's 53 and above what any decimal literal a human
// writes needs, and it is fixed rather than derived so that the same
// literal always produces the same bytes.
const bigFloatPrec = 256

// bigFloatArg builds pxf.BigFloat, matching protowire-go's
// marshalBigFloat: mantissa is the value scaled to an integer at `prec`
// bits, and exponent is the binary exponent adjusted by that scaling.
func bigFloatArg(text string) (*pxf.BigFloat, bool) {
	r, ok := bigRatFromText(text)
	if !ok {
		return nil, false
	}
	bf := new(big.Float).SetPrec(bigFloatPrec).SetRat(r)

	mant := new(big.Float).SetPrec(bigFloatPrec)
	exp := bf.MantExp(mant)
	mant.SetMantExp(mant, bigFloatPrec)
	mantInt, _ := mant.Int(nil)
	if mantInt.Sign() < 0 {
		mantInt.Neg(mantInt)
	}

	return &pxf.BigFloat{
		Mantissa: mantInt.Bytes(),
		Exponent: int32(exp) - int32(bigFloatPrec),
		Prec:     uint32(bigFloatPrec),
		Negative: bf.Signbit(),
	}, true
}
