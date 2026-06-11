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
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/runtime/protoimpl"

	"github.com/trendvidia/protocompile/ast"
	"github.com/trendvidia/protocompile/ast/predeclared"
	pwsv1 "github.com/trendvidia/protocompile/internal/gen/protowire/schema/v1"
	"github.com/trendvidia/protocompile/ir"
	"github.com/trendvidia/protocompile/seq"
	"github.com/trendvidia/protocompile/token"
	"github.com/trendvidia/protocompile/token/keyword"
)

// emitAnnotations attaches a [pwsv1.AnnotationList] extension to the
// given Options message when the carrier has at least one resolved
// annotation use site. Unresolved use sites (Target() == zero) are
// dropped — a B2 diagnostic was already emitted for those.
//
// Returns true when an extension was attached. Callers use that to
// ensure an Options message is materialised on the descriptor when
// it would otherwise have been nil.
func emitAnnotations(uses seq.Indexer[ir.AnnotationUse], target proto.Message, extDesc *protoimpl.ExtensionInfo) bool {
	if uses.Len() == 0 {
		return false
	}

	list := &pwsv1.AnnotationList{}
	for u := range seq.Values(uses) {
		if entry := buildAnnotation(u); entry != nil {
			list.Entries = append(list.Entries, entry)
		}
	}
	if len(list.Entries) == 0 {
		return false
	}
	proto.SetExtension(target, extDesc, list)
	return true
}

// buildAnnotation lowers one [ir.AnnotationUse] into a
// [pwsv1.Annotation]. Returns nil for an unresolved use site.
func buildAnnotation(u ir.AnnotationUse) *pwsv1.Annotation {
	target := u.Target()
	if target.IsZero() {
		return nil
	}

	ann := &pwsv1.Annotation{
		Name: string(target.FullName()),
	}

	params := target.Params()
	args := u.AST().Args()
	for i := range args.Len() {
		var param ir.AnnotationParam
		if i < params.Len() {
			param = params.At(i)
		}
		arg := buildArg(args.At(i), param)
		if arg != nil {
			ann.Args = append(ann.Args, arg)
		}
	}
	return ann
}

// buildArg lowers one argument expression into a
// [pwsv1.AnnotationArg]. The parameter (when known) drives type-
// specific lowering — e.g. routing string literals into bytes_value
// when the param is `bytes`, or wrapping the verbatim source into
// an [Expression] when the param is the `expression` pseudo-type.
//
// Returns nil for argument shapes the legalize pass should already
// have rejected (array/dict/range/field).
func buildArg(arg ast.ExprAny, param ir.AnnotationParam) *pwsv1.AnnotationArg {
	if arg.IsZero() {
		return nil
	}

	// `expression` and `any` params capture the verbatim source of
	// the argument (for expression params, that's exactly what the
	// engine will parse). Function-call extraction is deferred to a
	// follow-up; the source string alone is sufficient for the
	// engine to operate.
	if param.IsExpression() {
		return &pwsv1.AnnotationArg{
			Value: &pwsv1.AnnotationArg_Expression{
				Expression: &pwsv1.Expression{
					Source: arg.Span().Text(),
				},
			},
		}
	}

	switch arg.Kind() {
	case ast.ExprKindLiteral:
		return buildLiteralArg(arg.AsLiteral(), param)

	case ast.ExprKindPath:
		// Identifier path: model as Literal.enum_name. Covers both
		// enum-value references (`@scope(PUBLIC)`) and the boolean
		// keywords `true` / `false` when the param is bool.
		path := arg.AsPath().Path
		text := path.Canonicalized()
		if param.IsScalar() && param.Scalar() == predeclared.Bool {
			switch text {
			case "true":
				return &pwsv1.AnnotationArg{Value: &pwsv1.AnnotationArg_BoolValue{BoolValue: true}}
			case "false":
				return &pwsv1.AnnotationArg{Value: &pwsv1.AnnotationArg_BoolValue{BoolValue: false}}
			}
		}
		return &pwsv1.AnnotationArg{
			Value: &pwsv1.AnnotationArg_Literal{
				Literal: &pwsv1.Literal{
					Kind: &pwsv1.Literal_EnumName{EnumName: text},
				},
			},
		}

	case ast.ExprKindPrefixed:
		// `-N` numeric negation. The legalize pass rejects everything
		// else under a prefix.
		pref := arg.AsPrefixed()
		if pref.Prefix() != keyword.Sub {
			return nil
		}
		inner := pref.Expr()
		if inner.Kind() != ast.ExprKindLiteral {
			return nil
		}
		neg := buildLiteralArg(inner.AsLiteral(), param)
		if neg == nil {
			return nil
		}
		switch v := neg.Value.(type) {
		case *pwsv1.AnnotationArg_IntValue:
			v.IntValue = -v.IntValue
		case *pwsv1.AnnotationArg_DoubleValue:
			v.DoubleValue = -v.DoubleValue
		}
		return neg
	}
	return nil
}

// buildLiteralArg lowers a string-or-number literal into the
// appropriate AnnotationArg oneof variant. The parameter drives
// whether a string literal becomes string_value vs bytes_value, and
// whether a numeric literal becomes int_value vs double_value.
func buildLiteralArg(lit ast.ExprLiteral, param ir.AnnotationParam) *pwsv1.AnnotationArg {
	tok := lit.Token
	switch tok.Kind() {
	case token.String:
		text := tok.AsString().Text()
		if param.IsScalar() && param.Scalar() == predeclared.Bytes {
			return &pwsv1.AnnotationArg{
				Value: &pwsv1.AnnotationArg_BytesValue{BytesValue: []byte(text)},
			}
		}
		return &pwsv1.AnnotationArg{
			Value: &pwsv1.AnnotationArg_StringValue{StringValue: text},
		}

	case token.Number:
		num := tok.AsNumber()
		if param.IsScalar() && isFloatScalar(param.Scalar()) {
			f, _ := num.Float()
			return &pwsv1.AnnotationArg{
				Value: &pwsv1.AnnotationArg_DoubleValue{DoubleValue: f},
			}
		}
		// Default integer lowering. NumberToken.Int returns a uint64;
		// reinterpret via int64 to preserve two's-complement semantics
		// for the AnnotationArg.int_value field.
		u, _ := num.Int()
		return &pwsv1.AnnotationArg{
			Value: &pwsv1.AnnotationArg_IntValue{IntValue: int64(u)},
		}
	}
	return nil
}

// isFloatScalar reports whether the predeclared scalar is `float` or
// `double` — i.e., should be lowered into AnnotationArg.double_value.
func isFloatScalar(n predeclared.Name) bool {
	return n == predeclared.Float || n == predeclared.Double
}
