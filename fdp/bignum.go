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
	"errors"
	"math/big"
	"strings"

	pxf "github.com/trendvidia/protocompile/gen/pxf"
	"github.com/trendvidia/protocompile/internal/ext/bigx"
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
//
// None of the conversions may do work proportional to a literal's
// exponent (protowire HARDENING.md, "Arbitrary-precision magnitudes"):
// the text is a dozen bytes however large the value it names.

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

// bigIntArg builds pxf.BigInt. Integrality and the digit bound are read
// from the text (bigx.IntegerShape) before anything is built, so a
// literal like 1e999999999 is refused from its exponent rather than
// materialised (#210); the ir pass has diagnosed both cases, and the
// caller writes no value for a bigx.ErrRange. Within the bound the
// value is exact: the digits shifted by at most MaxNumericLiteralDigits
// decimal places.
func bigIntArg(text string) (*pxf.BigInt, error) {
	digits, integral, ok := bigx.IntegerShape(text)
	if !ok || !integral {
		return nil, errMalformedLiteral
	}
	if digits > ir.MaxNumericLiteralDigits {
		return nil, bigx.ErrRange
	}
	r, ok := bigRatFromText(text)
	if !ok || !r.IsInt() {
		return nil, errMalformedLiteral
	}
	i := r.Num()
	return &pxf.BigInt{
		Abs:      new(big.Int).Abs(i).Bytes(),
		Negative: i.Sign() < 0,
	}, nil
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
//
// The scale is bounded by ir.MaxNumericLiteralDigits in magnitude
// (protowire HARDENING.md): a decoder materialises 10^scale and MUST
// refuse one beyond the limit, so the ir pass diagnoses such a literal and
// this reports bigx.ErrRange for it, and the caller writes no value.
func decimalArg(text string) (*pxf.Decimal, error) {
	scale, ok := bigx.DecimalScale(text)
	if !ok {
		return nil, errMalformedLiteral
	}
	if scale > ir.MaxNumericLiteralDigits || scale < -ir.MaxNumericLiteralDigits {
		return nil, bigx.ErrRange
	}
	if n, ok := bigx.SignificantDigits(text); !ok || n > ir.MaxNumericLiteralDigits {
		if !ok {
			return nil, errMalformedLiteral
		}
		return nil, bigx.ErrRange
	}

	t := strings.ReplaceAll(text, "_", "")
	lower := strings.ToLower(t)

	// A based literal has no fractional part and no exponent.
	if strings.HasPrefix(lower, "0x") || strings.HasPrefix(lower, "0o") ||
		strings.HasPrefix(lower, "0b") {
		i, ok := new(big.Int).SetString(t, 0)
		if !ok {
			return nil, errMalformedLiteral
		}
		return &pxf.Decimal{
			Unscaled: new(big.Int).Abs(i).Bytes(),
			Negative: i.Sign() < 0,
		}, nil
	}

	mantissa := t
	if i := strings.IndexAny(t, "eE"); i != -1 {
		mantissa = t[:i]
	}
	if i := strings.IndexByte(mantissa, '.'); i != -1 {
		mantissa = mantissa[:i] + mantissa[i+1:]
	}
	if mantissa == "" || mantissa == "-" {
		return nil, errMalformedLiteral
	}

	unscaled, ok := new(big.Int).SetString(mantissa, 10)
	if !ok {
		return nil, errMalformedLiteral
	}
	return &pxf.Decimal{
		Unscaled: new(big.Int).Abs(unscaled).Bytes(),
		Scale:    int32(scale), // #nosec G115 -- bounded by MaxNumericLiteralDigits above
		Negative: unscaled.Sign() < 0,
	}, nil
}

// errMalformedLiteral is a literal the text parsers could not read; the
// ir pass has already diagnosed its shape, and the lowering falls back to
// the token's parsed form.
var errMalformedLiteral = errors.New("malformed numeric literal")

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
//
// The conversion is bigx.BigFloatLiteral's — floating point at 256 bits,
// never an exact rational, so a huge decimal exponent costs microseconds
// rather than materialising 10^n (#210). A literal the wire cannot hold
// (its binary exponent outside int32) is bigx.ErrRange: the ir pass has
// already diagnosed it, and the caller writes no value for it.
func bigFloatArg(text string) (*pxf.BigFloat, error) {
	mant, exp, neg, err := bigx.BigFloatLiteral(text, bigFloatPrec)
	if err != nil {
		return nil, err
	}
	return &pxf.BigFloat{
		Mantissa: mant.Bytes(),
		Exponent: exp,
		Prec:     uint32(bigFloatPrec),
		Negative: neg,
	}, nil
}
