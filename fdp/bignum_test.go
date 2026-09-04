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

package fdp_test

import (
	"fmt"
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	pwsv1 "github.com/trendvidia/protocompile/gen/protowire/schema/v1"
)

// pxfArg compiles `@default(lit)` on a field of the named pxf type and
// returns the lowered argument.
func pxfArg(t *testing.T, msg, lit string) *pwsv1.AnnotationArg {
	t.Helper()
	f := compileForFDPTest(t, fmt.Sprintf(`syntax = "proto3";
package pxf;

annotation default(value: any);

message %s { bytes abs = 1; bool negative = 2; }

message M {
  %s f = 1 @default(%s);
}
`, msg, msg, lit))
	for _, m := range f.GetMessageType() {
		if m.GetName() != "M" {
			continue
		}
		require.Len(t, m.GetField(), 1)
		list, _ := proto.GetExtension(
			m.GetField()[0].GetOptions(), pwsv1.E_FieldAnnotations).(*pwsv1.AnnotationList)
		require.NotNil(t, list, "field carries no AnnotationList extension")
		require.Len(t, list.GetEntries(), 1)
		require.Len(t, list.GetEntries()[0].GetArgs(), 1)
		return list.GetEntries()[0].GetArgs()[0]
	}
	t.Fatal("no message M")
	return nil
}

// TestBigIntArgIsExact is protowire#263 for pxf.BigInt.
//
// Before the new members existed these were a compile error, and before
// v0.31.0 they were a NEGATIVE int_value -- the two's-complement bits --
// on the one type in the language whose purpose is holding values above
// int64. The assertion is the exact value, decoded back from the bytes.
func TestBigIntArgIsExact(t *testing.T) {
	t.Parallel()

	for _, want := range []string{
		"0",
		"42",
		"-42",
		"9223372036854775807",  // MaxInt64
		"9223372036854775808",  // one past it
		"18446744073709551615", // MaxUint64
		"-18446744073709551615",
		"123456789012345678901234567890", // far beyond any fixed width
		"-123456789012345678901234567890",
	} {
		t.Run(want, func(t *testing.T) {
			t.Parallel()
			arg := pxfArg(t, "BigInt", want)
			v, ok := arg.Value.(*pwsv1.AnnotationArg_BigIntValue)
			require.True(t, ok, "want big_int_value, got %T", arg.Value)

			got := new(big.Int).SetBytes(v.BigIntValue.GetAbs())
			if v.BigIntValue.GetNegative() {
				got.Neg(got)
			}
			assert.Equal(t, want, got.String())
		})
	}
}

// TestBigIntArgTakesItsOwnMemberAtEverySize pins the decision from
// protowire#263: routing is per CARRIER, not per magnitude. The easy
// mistake is to send only large values to the new member, which would
// give a consumer two cases for one type.
func TestBigIntArgTakesItsOwnMemberAtEverySize(t *testing.T) {
	t.Parallel()

	for _, lit := range []string{"0", "1", "42", "-7", "9223372036854775807"} {
		arg := pxfArg(t, "BigInt", lit)
		assert.IsType(t, (*pwsv1.AnnotationArg_BigIntValue)(nil), arg.Value,
			"%s on a pxf.BigInt must take big_int_value, not int_value", lit)
	}
}

// TestBigIntArgAcceptsAFloatSpellingThatIsAnInteger: `1e19` is an integer
// written in floating-point notation, and the value is what matters.
func TestBigIntArgAcceptsAFloatSpellingThatIsAnInteger(t *testing.T) {
	t.Parallel()

	arg := pxfArg(t, "BigInt", "1e19")
	v, ok := arg.Value.(*pwsv1.AnnotationArg_BigIntValue)
	require.True(t, ok, "got %T", arg.Value)
	got := new(big.Int).SetBytes(v.BigIntValue.GetAbs())
	assert.Equal(t, "10000000000000000000", got.String())
}

// TestDecimalArgPreservesDeclaredScale pins bignum.proto's statement that
// the text form preserves exact scale -- `"1.00"` has scale 2.
//
// A big.Rat cannot carry that: it normalises 1.50 to 3/2, and the trailing
// zero, which is the author's statement of precision, is lost. Scale is
// therefore read from the literal's text.
func TestDecimalArgPreservesDeclaredScale(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		lit      string
		unscaled string
		scale    int32
		negative bool
	}{
		{"1.5", "15", 1, false},
		{"1.50", "150", 2, false}, // the trailing zero survives
		{"1.00", "100", 2, false}, // bignum.proto's own example
		{"-2.25", "225", 2, true},
		{"42", "42", 0, false},
		{"1.5e2", "15", -1, false}, // 15 x 10^1 = 150
		{"18446744073709551615", "18446744073709551615", 0, false},
		{"0.000000000000000000001", "1", 21, false},
	} {
		t.Run(tc.lit, func(t *testing.T) {
			t.Parallel()
			arg := pxfArg(t, "Decimal", tc.lit)
			v, ok := arg.Value.(*pwsv1.AnnotationArg_DecimalValue)
			require.True(t, ok, "want decimal_value, got %T", arg.Value)

			assert.Equal(t, tc.unscaled,
				new(big.Int).SetBytes(v.DecimalValue.GetUnscaled()).String())
			assert.Equal(t, tc.scale, v.DecimalValue.GetScale())
			assert.Equal(t, tc.negative, v.DecimalValue.GetNegative())
		})
	}
}

// TestBigFloatArgRoundTrips decodes the emitted mantissa and exponent back
// into a value and compares it to the literal.
//
// value = mantissa x 2^exponent, per protowire-go's marshalBigFloat, which
// already decodes this shape -- the bytes have to match what reads them,
// so they are checked rather than assumed.
func TestBigFloatArgRoundTrips(t *testing.T) {
	t.Parallel()

	for _, lit := range []string{
		"1.5",
		"-1.5",
		"0.5",
		"18446744073709551615",
		"1.2345678901234567890e19", // the value float64 rounded to ...67168
		"1e100",
	} {
		t.Run(lit, func(t *testing.T) {
			t.Parallel()
			arg := pxfArg(t, "BigFloat", lit)
			v, ok := arg.Value.(*pwsv1.AnnotationArg_BigFloatValue)
			require.True(t, ok, "want big_float_value, got %T", arg.Value)
			bf := v.BigFloatValue

			// value = mantissa x 2^exponent
			m := new(big.Float).SetPrec(uint(bf.GetPrec())).
				SetInt(new(big.Int).SetBytes(bf.GetMantissa()))
			got := new(big.Float).SetPrec(uint(bf.GetPrec())).
				SetMantExp(m, int(bf.GetExponent()))
			if bf.GetNegative() {
				got.Neg(got)
			}

			want, _, err := big.ParseFloat(lit, 10, uint(bf.GetPrec()), big.ToNearestEven)
			require.NoError(t, err)
			assert.Zero(t, want.Cmp(got),
				"%s: want %s, got %s", lit, want.Text('g', 30), got.Text('g', 30))
		})
	}
}

// TestBigFloatArgKeepsPrecisionFloat64Loses is the point of the type.
// Through double_value this literal came back as ...67168.
func TestBigFloatArgKeepsPrecisionFloat64Loses(t *testing.T) {
	t.Parallel()

	arg := pxfArg(t, "BigFloat", "1.2345678901234567890e19")
	wrapped, ok := arg.Value.(*pwsv1.AnnotationArg_BigFloatValue)
	require.True(t, ok, "want big_float_value, got %T", arg.Value)
	v := wrapped.BigFloatValue

	m := new(big.Float).SetPrec(uint(v.GetPrec())).
		SetInt(new(big.Int).SetBytes(v.GetMantissa()))
	got := new(big.Float).SetPrec(uint(v.GetPrec())).
		SetMantExp(m, int(v.GetExponent()))

	i, _ := got.Int(nil)
	assert.Equal(t, "12345678901234567890", i.String(),
		"a float64 rounds this to 12345678901234567168")
}
