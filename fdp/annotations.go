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
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/runtime/protoimpl"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/trendvidia/protocompile/ast"
	"github.com/trendvidia/protocompile/ast/predeclared"
	pwsv1 "github.com/trendvidia/protocompile/gen/protowire/schema/v1"
	"github.com/trendvidia/protocompile/ir"
	"github.com/trendvidia/protocompile/seq"
	"github.com/trendvidia/protocompile/source"
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
func emitAnnotations(
	uses seq.Indexer[ir.AnnotationUse],
	target proto.Message,
	extDesc *protoimpl.ExtensionInfo,
	carrier predeclared.Name,
) bool {
	list := buildAnnotationList(uses, carrier)
	if list == nil {
		return false
	}
	proto.SetExtension(target, extDesc, list)
	return true
}

// buildAnnotationList lowers a carrier's resolved annotation use sites
// into a [pwsv1.AnnotationList], or returns nil when none survive.
// Split out of [emitAnnotations] for the method carrier, which reads
// the list back for the §5.2 `google.api.http` lowering.
func buildAnnotationList(uses seq.Indexer[ir.AnnotationUse], carrier predeclared.Name) *pwsv1.AnnotationList {
	if uses.Len() == 0 {
		return nil
	}

	list := &pwsv1.AnnotationList{}
	for u := range seq.Values(uses) {
		// A declaration is not attached to a typed carrier.
		if entry := buildAnnotation(u, carrier); entry != nil {
			list.Entries = append(list.Entries, entry)
		}
	}
	if len(list.Entries) == 0 {
		return nil
	}
	return list
}

// buildAnnotation lowers one [ir.AnnotationUse] into a
// [pwsv1.Annotation]. Returns nil for an unresolved use site.
func buildAnnotation(u ir.AnnotationUse, carrier predeclared.Name) *pwsv1.Annotation {
	target := u.Target()
	if target.IsZero() {
		return nil
	}

	ann := &pwsv1.Annotation{
		Name:     string(target.FullName()),
		Location: locationOf(u.AST().At()),
	}

	for _, b := range u.ArgBindings() {
		arg := buildArg(u, b, carrier)
		if arg != nil {
			ann.Args = append(ann.Args, arg)
		}
	}
	return ann
}

// buildArg lowers one bound argument into a [pwsv1.AnnotationArg].
// The parameter drives type-specific lowering — e.g. routing string
// literals into bytes_value when the param is `bytes`, or keeping
// the verbatim capture (plus extracted calls) when the param is the
// `expression` pseudo-type.
//
// Returns nil for shapes the B3 classification pass already rejected
// (opaque captures on non-expression params, message literals).
func buildArg(u ir.AnnotationUse, b ir.AnnotationArgBinding, carrier predeclared.Name) *pwsv1.AnnotationArg {
	if b.Arg.IsZero() {
		return nil
	}

	out := &pwsv1.AnnotationArg{}
	if b.Arg.IsNamed() {
		out.Name = b.Arg.Name().Text()
	}

	// `expression` params keep the §5.1 capture verbatim (whitespace-
	// trimmed, quotes never stripped) together with the function-call
	// sites extracted per §8.1.
	if b.Param.IsExpression() {
		expr := &pwsv1.Expression{
			Source:   strings.TrimSpace(b.Arg.ValueSpan().Text()),
			Location: locationOf(b.Arg.ValueStart()),
		}
		for _, call := range u.ExtractCalls(b.Arg) {
			expr.Calls = append(expr.Calls, &pwsv1.FunctionRef{
				Fqn:   string(call.Target.FullName()),
				Arity: int32(call.Arity),
			})
		}
		out.Value = &pwsv1.AnnotationArg_Expression{Expression: expr}
		return out
	}

	// Message literals were evaluated against their resolved type by
	// the B3 pass; the carrier form is a google.protobuf.Any
	// serialized at lowering (RFC-001 §8.1).
	if b.Arg.Value().Kind() == ast.ExprKindDict {
		msg := u.MessageLiteralArg(b.Arg)
		if msg.IsZero() {
			return nil // Evaluation failed; already diagnosed.
		}
		out.Value = &pwsv1.AnnotationArg_Literal{Literal: messageLiteral(msg)}
		return out
	}

	value := buildArgValue(u, b.Arg.Value(), b.Param, carrier)
	if value == nil {
		return nil
	}
	value.Name = out.Name
	return value
}

// buildArgValue lowers a classified argument value expression into a
// [pwsv1.AnnotationArg] with only the value oneof populated. Returns
// nil for shapes B3 rejected.
func buildArgValue(u ir.AnnotationUse, value ast.ExprAny, param ir.AnnotationParam, carrier predeclared.Name) *pwsv1.AnnotationArg {
	switch value.Kind() {
	case ast.ExprKindLiteral:
		return buildLiteralArg(value.AsLiteral(), param, carrier)

	case ast.ExprKindPath:
		// Identifier path: the boolean keywords `true` / `false`, or
		// an enum-value reference.
		path := value.AsPath().Path
		switch path.Canonicalized() {
		case "true":
			return &pwsv1.AnnotationArg{Value: &pwsv1.AnnotationArg_BoolValue{BoolValue: true}}
		case "false":
			return &pwsv1.AnnotationArg{Value: &pwsv1.AnnotationArg_BoolValue{BoolValue: false}}
		}
		return &pwsv1.AnnotationArg{
			Value: &pwsv1.AnnotationArg_Literal{
				Literal: &pwsv1.Literal{
					Kind: &pwsv1.Literal_EnumValue{EnumValue: buildEnumLiteral(u, path)},
				},
			},
		}

	case ast.ExprKindPrefixed:
		// `-N` numeric negation; the classifier admits nothing else
		// under a prefix.
		pref := value.AsPrefixed()
		if pref.Prefix() != keyword.Sub {
			return nil
		}
		inner := pref.Expr()
		if inner.Kind() != ast.ExprKindLiteral {
			return nil
		}
		neg := buildLiteralArg(inner.AsLiteral(), param, carrier)
		if neg == nil {
			return nil
		}
		switch v := neg.Value.(type) {
		case *pwsv1.AnnotationArg_IntValue:
			// buildLiteralArg reinterpreted the magnitude via int64, so
			// negating it here is faithful only while the magnitude fits
			// int64. A magnitude in (MaxInt64, MaxUint64] reinterprets to a
			// negative int64, and negating THAT flips the sign back:
			// `-18446744073709551615` reached the carrier as `int_value: 1`.
			// No consumer recovers the literal from that, so it takes the
			// double route an out-of-uint64 literal already takes (#165).
			//
			// MinInt64 is excluded deliberately: 2^63 is its own two's
			// complement, so `-9223372036854775808` negates to itself and is
			// exactly representable.
			if v.IntValue < 0 && v.IntValue != math.MinInt64 {
				// The IntValue variant is only produced from a number token.
				f, _ := inner.AsLiteral().Token.AsNumber().Float()
				return &pwsv1.AnnotationArg{
					Value: &pwsv1.AnnotationArg_DoubleValue{DoubleValue: -f},
				}
			}
			v.IntValue = -v.IntValue
		case *pwsv1.AnnotationArg_DoubleValue:
			v.DoubleValue = -v.DoubleValue
		}
		return neg

	case ast.ExprKindArray:
		return &pwsv1.AnnotationArg{Value: buildListLiteral(u, value.AsArray(), param, carrier)}

	case ast.ExprKindDict:
		// A typed message-literal list element (RFC-001 §8.1,
		// LiteralValue.literal): evaluated against its explicit type
		// by the B3 pass and keyed by the dict's braces token.
		// Argument-level literals never reach here — [buildArg]
		// handles them before dispatching to this function.
		msg := u.MessageLiteralElem(value)
		if msg.IsZero() {
			return nil // Evaluation failed; already diagnosed.
		}
		return &pwsv1.AnnotationArg{
			Value: &pwsv1.AnnotationArg_Literal{Literal: messageLiteral(msg)},
		}
	}

	return nil
}

// messageLiteral serializes an evaluated message literal into the
// carrier's resolved form: a [pwsv1.Literal] holding a
// google.protobuf.Any serialized at lowering (RFC-001 §8.1).
func messageLiteral(msg ir.MessageValue) *pwsv1.Literal {
	return &pwsv1.Literal{
		Kind: &pwsv1.Literal_Message{Message: &anypb.Any{
			TypeUrl: "type.googleapis.com/" + string(msg.Type().FullName()),
			Value:   msg.Marshal(nil, nil),
		}},
	}
}

// buildEnumLiteral lowers an enum-value reference into a resolved
// [pwsv1.EnumLiteral] — enum type FQN, value name, and number — per
// RFC-001 §8.1 ("enum references are lowered resolved").
//
// When the reference does not resolve (possible only when the B3
// pass already diagnosed the file), the path text is carried
// verbatim in value_name with enum_type/number unset.
func buildEnumLiteral(u ir.AnnotationUse, path ast.Path) *pwsv1.EnumLiteral {
	if member := u.ResolveEnumValueArg(path); !member.IsZero() {
		return &pwsv1.EnumLiteral{
			EnumType:  string(member.Parent().FullName()),
			ValueName: member.Name(),
			Number:    member.Number(),
		}
	}
	return &pwsv1.EnumLiteral{ValueName: path.Canonicalized()}
}

// buildListLiteral lowers a list-literal argument into a
// [pwsv1.ListLiteral] of [pwsv1.LiteralValue] elements. Elements
// carry no names and can never be expressions; homogeneity was
// checked by the B3 pass.
func buildListLiteral(u ir.AnnotationUse, arr ast.ExprArray, param ir.AnnotationParam, carrier predeclared.Name) *pwsv1.AnnotationArg_Literal {
	list := &pwsv1.ListLiteral{}
	for elem := range seq.Values(arr.Elements()) {
		if lv := buildListElement(u, elem, param, carrier); lv != nil {
			list.Elements = append(list.Elements, lv)
		}
	}
	return &pwsv1.AnnotationArg_Literal{
		Literal: &pwsv1.Literal{Kind: &pwsv1.Literal_List{List: list}},
	}
}

// buildListElement lowers one list element into a
// [pwsv1.LiteralValue]. The field numbers of LiteralValue's oneof
// mirror AnnotationArg's, so the scalar lowering is shared and
// re-wrapped.
func buildListElement(u ir.AnnotationUse, elem ast.ExprAny, param ir.AnnotationParam, carrier predeclared.Name) *pwsv1.LiteralValue {
	arg := buildArgValue(u, elem, param, carrier)
	if arg == nil {
		return nil
	}
	switch v := arg.Value.(type) {
	case *pwsv1.AnnotationArg_StringValue:
		return &pwsv1.LiteralValue{Kind: &pwsv1.LiteralValue_StringValue{StringValue: v.StringValue}}
	case *pwsv1.AnnotationArg_IntValue:
		return &pwsv1.LiteralValue{Kind: &pwsv1.LiteralValue_IntValue{IntValue: v.IntValue}}
	case *pwsv1.AnnotationArg_DoubleValue:
		return &pwsv1.LiteralValue{Kind: &pwsv1.LiteralValue_DoubleValue{DoubleValue: v.DoubleValue}}
	case *pwsv1.AnnotationArg_BoolValue:
		return &pwsv1.LiteralValue{Kind: &pwsv1.LiteralValue_BoolValue{BoolValue: v.BoolValue}}
	case *pwsv1.AnnotationArg_BytesValue:
		return &pwsv1.LiteralValue{Kind: &pwsv1.LiteralValue_BytesValue{BytesValue: v.BytesValue}}
	case *pwsv1.AnnotationArg_Literal:
		return &pwsv1.LiteralValue{Kind: &pwsv1.LiteralValue_Literal{Literal: v.Literal}}
	}
	return nil
}

// buildLiteralArg lowers a string-or-number literal into the
// appropriate AnnotationArg oneof variant. The parameter drives
// whether a string literal becomes string_value vs bytes_value, and
// whether a numeric literal becomes int_value vs double_value.
//
// Numeric routing has two cases that carry no declared scalar to type
// against: a zero param (function options, which have no declared
// parameter at all) and an `any` param (which accepts any
// literal-shaped argument). Both follow the literal's own spelling,
// so `1.5` keeps its fraction instead of truncating to `1`.
func buildLiteralArg(lit ast.ExprLiteral, param ir.AnnotationParam, carrier predeclared.Name) *pwsv1.AnnotationArg {
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
		// An untyped parameter is one that declares no scalar to
		// convert towards: no parameter at all, or `any`. Deliberately
		// not `!param.IsScalar()` — that would also capture enum- and
		// message-typed parameters, changing what an already-typed
		// parameter accepts.
		untyped := param.IsZero() || param.IsAny()

		// An untyped parameter has no type of its own, but the thing the
		// annotation is ATTACHED to usually does — `@default(1e19)` on a
		// `double` field is a value for that field. Routing by the
		// carrier's type is what keeps the band above MaxInt64 readable:
		// int_value stores it two's-complement, which only an unsigned
		// target knows to undo, so a double target received a negative
		// number (#172).
		//
		// Only floats need this. An integer carrier already recovers the
		// value from its own type, and a carrier with no scalar type —
		// a message, a service — falls back to the literal's spelling
		// because there is nothing else to consult.
		if param.IsScalar() && isFloatScalar(param.Scalar()) ||
			untyped && isFloatScalar(carrier) ||
			untyped && num.IsFloat() {
			f, _ := num.Float()
			return &pwsv1.AnnotationArg{
				Value: &pwsv1.AnnotationArg_DoubleValue{DoubleValue: f},
			}
		}
		// Int reports whether the conversion was exact, and it is false
		// precisely when the value does not fit — the big-integer path
		// saturates to MaxUint64 and says so. An untyped parameter has no
		// declared type to convert towards, so a literal that is not an
		// integer is simply not one, and follows the same spelling rule
		// the float case above uses.
		//
		// Without this the saturated value is written as the author's, and
		// `@default(1e100)` reaches the carrier as `int_value: -1` —
		// indistinguishable from `@default(18446744073709551615)`, which
		// means it exactly.
		u, exact := num.Int()
		if untyped && !exact {
			f, _ := num.Float()
			return &pwsv1.AnnotationArg{
				Value: &pwsv1.AnnotationArg_DoubleValue{DoubleValue: f},
			}
		}

		// Default integer lowering. NumberToken.Int returns a uint64;
		// reinterpret via int64 to preserve two's-complement semantics
		// for the AnnotationArg.int_value field.
		//
		// A declared integer parameter still takes an inexact value here,
		// wrapped. That is a narrower question — the literal does not fit
		// the type the author asked for, which wants a diagnostic rather
		// than a different silent lowering — and is pinned in
		// TestAnnotationNumericRouting rather than changed here (#165).
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
		Name:     string(ann.FullName()),
		Location: locationOf(ann.AST().Name()),
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
		out.DefaultValue = buildDefaultArg(deflt, p)
	}
	return out
}

// buildDefaultArg lowers a parameter's default-value expression.
// Defaults are declaration-site expressions (not use-site captures);
// enum-value references resolve relative to the annotation
// declaration's own scope and lower into the resolved [pwsv1.EnumLiteral]
// form, same as use-site arguments.
func buildDefaultArg(deflt ast.ExprAny, param ir.AnnotationParam) *pwsv1.AnnotationArg {
	if param.IsExpression() {
		return &pwsv1.AnnotationArg{
			Value: &pwsv1.AnnotationArg_Expression{
				Expression: &pwsv1.Expression{
					Source:   strings.TrimSpace(deflt.Span().Text()),
					Location: spanLocation(deflt.Span()),
				},
			},
		}
	}

	if deflt.Kind() == ast.ExprKindPath {
		path := deflt.AsPath().Path
		switch path.Canonicalized() {
		case "true":
			return &pwsv1.AnnotationArg{Value: &pwsv1.AnnotationArg_BoolValue{BoolValue: true}}
		case "false":
			return &pwsv1.AnnotationArg{Value: &pwsv1.AnnotationArg_BoolValue{BoolValue: false}}
		}
		lit := &pwsv1.EnumLiteral{ValueName: path.Canonicalized()}
		if member := param.Annotation().ResolveEnumValueDefault(path); !member.IsZero() {
			lit = &pwsv1.EnumLiteral{
				EnumType:  string(member.Parent().FullName()),
				ValueName: member.Name(),
				Number:    member.Number(),
			}
		}
		return &pwsv1.AnnotationArg{
			Value: &pwsv1.AnnotationArg_Literal{
				Literal: &pwsv1.Literal{
					Kind: &pwsv1.Literal_EnumValue{EnumValue: lit},
				},
			},
		}
	}

	// A parameter DEFAULT belongs to the declaration, not to anything it
	// is attached to, so there is no carrier type to route by.
	return buildArgValue(ir.AnnotationUse{}, deflt, param, predeclared.Unknown)
}

// locationOf packs a token's start position into a
// [pwsv1.SourceLocation]. Returns nil for a zero token (e.g. a
// synthesized use with no source form), leaving the field unset.
func locationOf(tok token.Token) *pwsv1.SourceLocation {
	if tok.IsZero() {
		return nil
	}
	return spanLocation(tok.Span())
}

// spanLocation packs a span's start position into a
// [pwsv1.SourceLocation]. The span's own file wins: for an
// alias-propagated use, the location points into the alias's
// defining file, not the consuming one.
func spanLocation(span source.Span) *pwsv1.SourceLocation {
	if span.IsZero() {
		return nil
	}
	return sourceLocation(span, span.StartLoc())
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
		Name:     string(fn.FullName()),
		Location: locationOf(fn.AST().Name()),
	}
	for p := range seq.Values(fn.Params()) {
		out.Params = append(out.Params, &pwsv1.FunctionParam{
			Name: p.Name(),
			Type: p.TypeName(),
		})
	}
	// Bracket-form options lower AnnotationArg-shaped, keyed by the
	// unqualified option name. There is no declared parameter to type
	// against, so the zero param routes buildArgValue by the value's
	// own spelling, and the zero use leaves enum-value references
	// unresolved (carried verbatim in value_name).
	for opt := range seq.Values(fn.Options()) {
		if opt.Name == "" {
			continue
		}
		// A function option has no carrier either; its routing stays on
		// the literal's own spelling.
		arg := buildArgValue(ir.AnnotationUse{}, opt.Value, ir.AnnotationParam{}, predeclared.Unknown)
		if arg == nil {
			continue
		}
		if out.Options == nil {
			out.Options = make(map[string]*pwsv1.AnnotationArg)
		}
		out.Options[opt.Name] = arg
	}
	return out
}

// emitFileTypeDecls populates the [pwsv1.FileTypeDecls] extension
// on the file's Options with one [pwsv1.TypeDecl] per `type`
// alias declaration originating in the file. Returns true when at
// least one declaration was emitted.
func emitFileTypeDecls(file *ir.File, target *descriptorpb.FileOptions) bool {
	aliases := file.TypeAliases()
	if aliases.Len() == 0 {
		return false
	}
	out := &pwsv1.FileTypeDecls{}
	for a := range seq.Values(aliases) {
		out.Declarations = append(out.Declarations, buildTypeDecl(a))
	}
	if len(out.Declarations) == 0 {
		return false
	}
	proto.SetExtension(target, pwsv1.E_TypeDecls, out)
	return true
}

// buildTypeDecl lowers one [ir.TypeAlias] into the [pwsv1.TypeDecl]
// descriptor form. The base type lowers resolved — base_type_fqn is
// normatively fully qualified, so consumers get resolution-free
// reads even for bases written bare in source. The alias's
// annotation list (the `type Email = string @validate(...)`
// trailing-annotation form) is preserved verbatim so downstream
// tools can attach the validation rules at every use site without
// re-walking the IR.
func buildTypeDecl(a ir.TypeAlias) *pwsv1.TypeDecl {
	out := &pwsv1.TypeDecl{
		Name:        string(a.FullName()),
		BaseTypeFqn: a.BaseTypeFQN(),
		Location:    locationOf(a.AST().Name()),
	}
	uses := a.Annotations()
	if uses.Len() == 0 {
		return out
	}
	list := &pwsv1.AnnotationList{}
	for u := range seq.Values(uses) {
		// A declaration is not attached to a typed carrier.
		if entry := buildAnnotation(u, predeclared.Unknown); entry != nil {
			list.Entries = append(list.Entries, entry)
		}
	}
	if len(list.Entries) > 0 {
		out.Annotations = list
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

// wrapperScalars maps the nine google.protobuf wrapper messages to the
// scalar each one wraps.
//
// A wrapper is a message, so [ir.Type.Predeclared] reports nothing for a
// field declared as one, and [carrierScalar] would otherwise treat a
// `google.protobuf.DoubleValue` field as having no type to route by. They
// are well-known and their contents are fixed, so the mapping is a table
// rather than a structural inspection.
var wrapperScalars = map[string]predeclared.Name{
	"google.protobuf.DoubleValue": predeclared.Double,
	"google.protobuf.FloatValue":  predeclared.Float,
	"google.protobuf.Int64Value":  predeclared.Int64,
	"google.protobuf.UInt64Value": predeclared.UInt64,
	"google.protobuf.Int32Value":  predeclared.Int32,
	"google.protobuf.UInt32Value": predeclared.UInt32,
	"google.protobuf.BoolValue":   predeclared.Bool,
	"google.protobuf.StringValue": predeclared.String,
	"google.protobuf.BytesValue":  predeclared.Bytes,
}

// carrierScalar gives the scalar type an annotation attached to a member of
// this type should be routed by, or [predeclared.Unknown] when there is
// none to route by.
//
// A wrapper resolves to the scalar it wraps: `google.protobuf.DoubleValue`
// is the canonical nullable double, and a `@default` on one is a value for
// that double. Without this the wrappers kept the spelling route and with
// it the (MaxInt64, MaxUint64] ambiguity #172 removed from bare scalars
// (#174).
//
// The arbitrary-precision types protowire defines — `pxf.BigInt`,
// `pxf.Decimal`, `pxf.BigFloat` — are deliberately NOT mapped. They exist
// to hold values a `double` cannot represent, so routing them through
// `double_value` would lose the precision that is their entire purpose;
// a literal in the band still lowers ambiguously on those carriers, which
// is a known gap rather than an oversight. Resolving it needs a carrier
// field that can hold them, not a different scalar route.
func carrierScalar(t ir.Type) predeclared.Name {
	if n := t.Predeclared(); n != predeclared.Unknown {
		return n
	}
	if !t.IsMessage() {
		return predeclared.Unknown
	}
	return wrapperScalars[string(t.FullName())]
}
