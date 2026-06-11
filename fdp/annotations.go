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
	"google.golang.org/protobuf/types/descriptorpb"

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

// emitFileAnnotationDecls populates the [pwsv1.FileAnnotationDecls]
// extension on the file's Options with one [pwsv1.AnnotationDecl]
// per `annotation` declaration originating in the file.
//
// Returns true when at least one declaration was emitted; callers
// use that to decide whether the FileOptions wrapper needs to be
// retained on the descriptor.
func emitFileAnnotationDecls(file *ir.File, target *descriptorpb.FileOptions) bool {
	anns := file.Annotations()
	if anns.Len() == 0 {
		return false
	}

	out := &pwsv1.FileAnnotationDecls{}
	for ann := range seq.Values(anns) {
		out.Declarations = append(out.Declarations, buildAnnotationDecl(ann))
	}
	if len(out.Declarations) == 0 {
		return false
	}
	proto.SetExtension(target, pwsv1.E_AnnotationDecls, out)
	return true
}

// buildAnnotationDecl lowers one [ir.Annotation] into the
// [pwsv1.AnnotationDecl] descriptor form.
func buildAnnotationDecl(ann ir.Annotation) *pwsv1.AnnotationDecl {
	out := &pwsv1.AnnotationDecl{
		Name: string(ann.FullName()),
	}
	for p := range seq.Values(ann.Params()) {
		out.Params = append(out.Params, buildAnnotationParamDecl(p))
	}
	return out
}

// buildAnnotationParamDecl lowers one [ir.AnnotationParam] into the
// [pwsv1.AnnotationParam] descriptor form. The classification fields
// (B3) drive the [pwsv1.ParamType] selection; for user types, the
// type's fully-qualified name is recorded in `type_fqn`. Default-
// value lowering is deferred (paired with the broader default-
// expression lowering follow-up).
func buildAnnotationParamDecl(p ir.AnnotationParam) *pwsv1.AnnotationParam {
	out := &pwsv1.AnnotationParam{Name: p.Name()}
	switch {
	case p.IsExpression():
		out.Type = pwsv1.ParamType_EXPRESSION
	case p.IsAny():
		out.Type = pwsv1.ParamType_ANY
	case p.IsScalar():
		out.Type = scalarParamType(p.Scalar())
	default:
		// User-defined type or unresolved. UserType().IsZero()
		// distinguishes those: a resolved Type goes in type_fqn;
		// unresolved leaves the param at PARAM_TYPE_UNSPECIFIED
		// (a B3 diagnostic was already emitted).
		ut := p.UserType()
		if !ut.IsZero() {
			out.Type = pwsv1.ParamType_ENUM_OR_MESSAGE
			out.TypeFqn = string(ut.FullName())
		}
	}
	if deflt := p.Default(); !deflt.IsZero() {
		out.DefaultValue = buildArg(deflt, p)
	}
	return out
}

// emitFileFunctions populates the [pwsv1.FileFunctions] extension
// on the file's Options with one [pwsv1.FunctionDecl] per
// `function` declaration originating in the file. Returns true when
// at least one declaration was emitted.
func emitFileFunctions(file *ir.File, target *descriptorpb.FileOptions) bool {
	fns := file.Functions()
	if fns.Len() == 0 {
		return false
	}
	out := &pwsv1.FileFunctions{}
	for fn := range seq.Values(fns) {
		out.Declarations = append(out.Declarations, buildFunctionDecl(fn))
	}
	if len(out.Declarations) == 0 {
		return false
	}
	proto.SetExtension(target, pwsv1.E_Functions, out)
	return true
}

// buildFunctionDecl lowers one [ir.Function] into the
// [pwsv1.FunctionDecl] descriptor form. Per the PSE design, the
// engine performs run-time type-checking against the function's
// signature; the descriptor surface just records names and textual
// type references.
func buildFunctionDecl(fn ir.Function) *pwsv1.FunctionDecl {
	out := &pwsv1.FunctionDecl{
		Name: string(fn.FullName()),
	}
	for p := range seq.Values(fn.Params()) {
		out.Params = append(out.Params, &pwsv1.FunctionParam{
			Name: p.Name(),
			Type: p.TypeName(),
		})
	}
	return out
}

// scalarParamType maps a predeclared scalar [predeclared.Name] to
// the matching [pwsv1.ParamType] value. Returns
// [pwsv1.ParamType_PARAM_TYPE_UNSPECIFIED] for non-scalars (which
// shouldn't reach this function — call sites gate on IsScalar()).
func scalarParamType(n predeclared.Name) pwsv1.ParamType {
	switch n {
	case predeclared.String:
		return pwsv1.ParamType_STRING
	case predeclared.Int32, predeclared.SInt32, predeclared.Fixed32, predeclared.SFixed32, predeclared.UInt32:
		return pwsv1.ParamType_INT32
	case predeclared.Int64, predeclared.SInt64, predeclared.Fixed64, predeclared.SFixed64, predeclared.UInt64:
		return pwsv1.ParamType_INT64
	case predeclared.Float:
		return pwsv1.ParamType_FLOAT
	case predeclared.Double:
		return pwsv1.ParamType_DOUBLE
	case predeclared.Bool:
		return pwsv1.ParamType_BOOL
	case predeclared.Bytes:
		return pwsv1.ParamType_BYTES
	}
	return pwsv1.ParamType_PARAM_TYPE_UNSPECIFIED
}
