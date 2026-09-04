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
	"github.com/trendvidia/protocompile/ast/predeclared"
	"github.com/trendvidia/protocompile/token"
)

// This file holds the conversion rule for an annotation argument bound to
// an `any` parameter, shared by the lowering (which needs the member) and
// the diagnostics (which need the fault). RFC-001 states it:
//
//	An argument is typed by its own literal, and is then converted to the
//	type of the element the annotation is attached to. A literal that
//	cannot be converted — in kind or in range — is a compile error. Where
//	there is no annotated element type, the literal's own type stands.
//
// Sharing it is the point. checkCarrierRange and buildLiteralArg used to
// MIRROR each other, each carrying a comment saying so and a test pinning
// the pair; every issue in the #165–#187 family was a cell where the two
// had drifted or where neither had an answer.

// ArgLiteralKind is the type an annotation argument carries on its own,
// before any target is considered.
type ArgLiteralKind int

const (
	// ArgLiteralNone is a shape this rule does not decide: an enum
	// reference, a message literal, a list.
	ArgLiteralNone  ArgLiteralKind = iota
	ArgLiteralInt                  // A number written without a point or an exponent.
	ArgLiteralFloat                // A number written with either.
	ArgLiteralString
	ArgLiteralBool
)

// ArgMember names the AnnotationArg oneof member a converted argument
// lands in.
type ArgMember int

const (
	ArgMemberNone ArgMember = iota
	ArgMemberInt
	ArgMemberDouble
	ArgMemberBool
	ArgMemberString
	ArgMemberBytes
	ArgMemberBigInt
	ArgMemberDecimal
	ArgMemberBigFloat
)

// String names the member as a consumer sees it, for diagnostics.
func (m ArgMember) String() string {
	switch m {
	case ArgMemberInt:
		return "int_value"
	case ArgMemberDouble:
		return "double_value"
	case ArgMemberBool:
		return "bool_value"
	case ArgMemberString:
		return "string_value"
	case ArgMemberBytes:
		return "bytes_value"
	case ArgMemberBigInt:
		return "big_int_value"
	case ArgMemberDecimal:
		return "decimal_value"
	case ArgMemberBigFloat:
		return "big_float_value"
	}
	return "none"
}

// String names the literal's own type as an author wrote it.
func (k ArgLiteralKind) String() string {
	switch k {
	case ArgLiteralInt:
		return "integer"
	case ArgLiteralFloat:
		return "floating-point"
	case ArgLiteralString:
		return "string"
	case ArgLiteralBool:
		return "boolean"
	}
	return "unknown"
}

// ConvertArgKind reports which member a literal of kind lands in once
// converted to target, and whether such a conversion exists.
//
// target is [predeclared.Unknown] when there is nothing to convert to:
// an annotation on a message, a service or a file, or on a member whose
// type is a message with no scalar reading. The literal's own type then
// stands, which is what RFC-001 means by the argument carrying its own
// typing.
//
// A false second return is a KIND mismatch and is always an error — a
// string is not a number however it is spelled. Range and integrality are
// separate questions, asked only once the kind fits, because they depend
// on the value rather than on how it was written.
func ConvertArgKind(kind ArgLiteralKind, target predeclared.Name, pxf PxfNumber) (ArgMember, bool) {
	own := func() ArgMember {
		switch kind {
		case ArgLiteralInt:
			return ArgMemberInt
		case ArgLiteralFloat:
			return ArgMemberDouble
		case ArgLiteralString:
			return ArgMemberString
		case ArgLiteralBool:
			return ArgMemberBool
		}
		return ArgMemberNone
	}

	if kind == ArgLiteralNone {
		return ArgMemberNone, true
	}

	// An arbitrary-precision carrier takes its own member for any numeric
	// literal, whatever the magnitude — see Type.PxfNumber. It is checked
	// before `target`, because CarrierScalar deliberately reports Unknown
	// for these three and the literal's own type is exactly what must not
	// stand here.
	if pxf != PxfNone {
		if kind == ArgLiteralInt || kind == ArgLiteralFloat {
			switch pxf {
			case PxfBigInt:
				return ArgMemberBigInt, true
			case PxfDecimal:
				return ArgMemberDecimal, true
			case PxfBigFloat:
				return ArgMemberBigFloat, true
			}
		}
		// A string or a bool is no more a value for one of these than for
		// an int32, and falls through to the kind mismatch below.
		return own(), false
	}

	if target == predeclared.Unknown {
		return own(), true
	}

	switch {
	case target.IsFloat():
		// Every numeric literal converts to a float; the float32 width
		// check is a range question, asked separately.
		if kind == ArgLiteralInt || kind == ArgLiteralFloat {
			return ArgMemberDouble, true
		}

	case target.IsNumber():
		// An integer target. A float-spelled literal converts when it is
		// integral and in range — `1e2` is 100 — so the member follows the
		// TARGET, not the spelling.
		if kind == ArgLiteralInt || kind == ArgLiteralFloat {
			return ArgMemberInt, true
		}

	case target == predeclared.Bool:
		if kind == ArgLiteralBool {
			return ArgMemberBool, true
		}

	case target == predeclared.String:
		if kind == ArgLiteralString {
			return ArgMemberString, true
		}

	case target == predeclared.Bytes:
		// `bytes` is what carries content a `string` cannot (#179, #184).
		if kind == ArgLiteralString {
			return ArgMemberBytes, true
		}

	default:
		// A target this rule has no reading for keeps the literal's own
		// type rather than rejecting it.
		return own(), true
	}

	return own(), false
}

// ArgLiteralKindOf classifies a scalar literal token by how it is
// WRITTEN, which is what RFC-001 means by the argument carrying its own
// typing. Boolean arguments are identifier paths rather than literal
// tokens, so callers classify those themselves.
//
// [token.NumberToken.IsFloat] is the spelling test: a `.` or an exponent
// makes a float. It did not count a positive exponent until #191, which is
// why this briefly needed a second predicate of its own.
func ArgLiteralKindOf(tok token.Token) ArgLiteralKind {
	switch tok.Kind() {
	case token.String:
		return ArgLiteralString
	case token.Number:
		if tok.AsNumber().IsFloat() {
			return ArgLiteralFloat
		}
		return ArgLiteralInt
	}
	return ArgLiteralNone
}
