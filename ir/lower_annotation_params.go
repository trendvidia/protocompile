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
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/trendvidia/protocompile/ast"
	"github.com/trendvidia/protocompile/ast/predeclared"
	"github.com/trendvidia/protocompile/internal/taxa"
	"github.com/trendvidia/protocompile/report"
	"github.com/trendvidia/protocompile/seq"
	"github.com/trendvidia/protocompile/token"
	"github.com/trendvidia/protocompile/token/keyword"
)

// resolveAnnotationParamTypes classifies the declared type on each
// [AnnotationParam] in the file as one of: a predeclared scalar
// (string, int32, …), the special PSE pseudo-type `expression`, the
// special PSE pseudo-type `any`, or a path to a user-defined message
// or enum (resolved through the file's imported symbol table).
//
// Phase B3 of the PSE annotation work. Runs after
// [resolveAnnotationUses]; the use-site argument validator
// [validateAnnotationUseArgs] consumes the classification.
func resolveAnnotationParamTypes(file *File, r *report.Report) {
	for ann := range seq.Values(file.Annotations()) {
		for p := range seq.Values(ann.Params()) {
			classifyAnnotationParamType(file, r, p)
		}
	}
}

// validateAnnotationParamDefaults type-checks each parameter's
// default expression against the parameter's classified type. Same
// shape rules as use-site arguments — see [validateAnnotationArg].
//
// Runs after [resolveAnnotationParamTypes] so the classification is
// already populated.
func validateAnnotationParamDefaults(file *File, r *report.Report) {
	for ann := range seq.Values(file.Annotations()) {
		for p := range seq.Values(ann.Params()) {
			deflt := p.Default()
			if deflt.IsZero() {
				continue
			}
			validateAnnotationArg(r, ann, p, deflt)
		}
	}
}

// classifyAnnotationParamType reads `p`'s declared type AST, fills
// in the classification fields on its [rawAnnotationParam], and
// emits a diagnostic when the type doesn't resolve.
//
// Only [ast.TypeKindPath] types are recognised today — the PSE v1
// parameter grammar doesn't admit prefixed or generic types. A
// non-path type results in an unresolved param plus a diagnostic.
func classifyAnnotationParamType(file *File, r *report.Report, p AnnotationParam) {
	raw := p.Raw()
	declType := p.AST().Type()
	if declType.IsZero() {
		return
	}

	if declType.Kind() != ast.TypeKindPath {
		r.Errorf("annotation parameter type must be a name").Apply(
			report.Snippetf(declType, "this type shape is not allowed for annotation parameters"),
			report.Helpf("PSE v1 annotation parameter types are limited to "+
				"the predeclared scalars (e.g. `string`, `int32`), the "+
				"pseudo-types `expression` or `any`, or a path to a "+
				"user-defined message/enum"),
		)
		return
	}

	path := declType.AsPath().Path
	text := path.Canonicalized()
	raw.typeName = file.session.intern.Intern(text)

	// Single-identifier paths cover all the special-case names.
	if first, isSingle := isSingleIdent(path); isSingle {
		switch first {
		case "expression":
			raw.isExpression = true
			return
		case "any":
			raw.isAny = true
			return
		}

		if scalar := predeclared.Lookup(first); scalar.IsScalar() {
			raw.scalar = scalar
			return
		}
	}

	// Otherwise treat the path as a user type lookup. Resolution
	// gates on SymbolKindMessage/SymbolKindEnum; anything else (e.g.
	// the user pointed `param: SomeService`) produces a "expected
	// message type" diagnostic via the standard symbolRef pipeline.
	sym := symbolRef{
		File:   file,
		Report: r,
		scope:  p.Annotation().FullName().Parent(),
		name:   FullName(text),
		span:   path,
		accept: func(k SymbolKind) bool {
			return k == SymbolKindMessage || k == SymbolKindEnum
		},
		want: taxa.MessageType,
	}.resolve()

	if !sym.IsZero() {
		raw.userType = Ref[Symbol]{
			file: refFileFor(file, sym.Context()),
			id:   sym.ID(),
		}
		if sym.Context() != file {
			file.imports.MarkUsed(sym.Context())
		}
	}
}

// isSingleIdent returns the single component text of a path that has
// exactly one component and no leading dot. Reports false otherwise.
func isSingleIdent(path ast.Path) (string, bool) {
	if path.IsZero() {
		return "", false
	}
	var count int
	var only string
	for c := range path.Components() {
		if !c.Separator().IsZero() {
			return "", false
		}
		ident := c.AsIdent()
		if ident.IsZero() {
			return "", false
		}
		count++
		only = ident.Name()
		if count > 1 {
			return "", false
		}
	}
	return only, count == 1
}

// validateAnnotationUseArgs checks each [AnnotationUse]'s argument
// list against its target [Annotation]'s parameter signature —
// RFC-001 §5.1's link-time classification of the capture-then-
// classify argument grammar.
//
// Phase B3 validation. Runs after [resolveAnnotationParamTypes].
//
// Checks performed:
//
//   - Binding structure: positional arguments must precede named
//     ones; named arguments must match a declared parameter; no
//     parameter may be bound twice; the positional count must not
//     exceed the parameter count; every parameter without a default
//     must be bound (covers `@name()`, the bare `@name` form, and
//     partial argument lists alike).
//   - `expression` params keep the capture verbatim; the function-
//     call sites extracted from the capture are arity-checked
//     against their resolved `function` declarations.
//   - Every other param requires the argument to have re-parsed
//     under `literal | qualifiedIdent | listLiteral |
//     messageLiteral`, then checks that shape against the param's
//     classified type (scalar/enum/message/any), including list-
//     literal homogeneity and the message-literal explicit-typing
//     rule.
func validateAnnotationUseArgs(file *File, r *report.Report) {
	for u, carrier := range allAnnotationUses(file) {
		target := u.Target()
		if target.IsZero() {
			continue // Already diagnosed in B2.
		}

		params := target.Params()
		bindings := u.ArgBindings()

		var sawNamed bool
		var positional int
		bound := make(map[string]ast.AnnotationUseArg, len(bindings))
		for _, b := range bindings {
			if b.Arg.IsNamed() {
				sawNamed = true
				if b.Param.IsZero() {
					r.Errorf("unknown named argument %q for `%s`",
						b.Arg.Name().Text(), target.FullName(),
					).Apply(
						report.Snippet(b.Arg.Name()),
						report.Snippetf(target.AST().Name(), "declared here"),
					)
					continue
				}
			} else {
				positional++
				if sawNamed {
					r.Errorf("positional argument after named argument").Apply(
						report.Snippet(b.Arg),
						report.Notef("positional arguments must precede all named arguments"),
					)
				}
			}

			if b.Param.IsZero() {
				continue
			}
			if prev, ok := bound[b.Param.Name()]; ok {
				r.Errorf("argument %q for `%s` bound more than once",
					b.Param.Name(), target.FullName(),
				).Apply(
					report.Snippet(b.Arg),
					report.Snippetf(prev, "first bound here"),
				)
				continue
			}
			bound[b.Param.Name()] = b.Arg
		}

		if positional > params.Len() {
			r.Errorf("too many arguments for `%s`: got %d, want at most %d",
				target.FullName(), positional, params.Len(),
			).Apply(
				report.Snippet(u.AST()),
				report.Snippetf(target.AST().Name(), "declared here"),
			)
		}

		for p := range seq.Values(params) {
			if !p.Default().IsZero() {
				continue
			}
			if _, ok := bound[p.Name()]; ok {
				continue
			}
			r.Errorf("missing required argument %q for `%s`",
				p.Name(), target.FullName(),
			).Apply(
				report.Snippet(u.AST()),
				report.Snippetf(p.AST(), "parameter declared here"),
				report.Notef("parameters without a default value are required"),
			)
		}

		for _, b := range bindings {
			if !b.Param.IsZero() {
				validateAnnotationUseArg(r, u, target, b, carrier)
				validateReservedSensitiveClass(r, target, b)
			}
		}
	}
}

// canonicalSensitiveFQN is the canonical `@sensitive` annotation
// declared in protowire/schema/v1/annotations.proto. Canonical-
// annotation semantic checks key on this resolved FQN, so a user
// annotation that happens to be named `sensitive` is unaffected.
const canonicalSensitiveFQN FullName = "protowire.schema.v1.sensitive"

// reservedClassNamespace is the class-name namespace RFC-001 §6.7
// rule 1 reserves for future spec-defined sensitivity classes,
// mirroring the §7 violation-code reservation.
const reservedClassNamespace = "protowire"

// validateReservedSensitiveClass rejects a `class` argument on the
// canonical `@sensitive` annotation whose value sits in the reserved
// `protowire.` namespace — an exact `"protowire"` or any dotted
// extension of it.
func validateReservedSensitiveClass(r *report.Report, target Annotation, b AnnotationArgBinding) {
	if target.FullName() != canonicalSensitiveFQN || b.Param.Name() != "class" {
		return
	}

	class, value, ok := stringArg(b)
	if !ok {
		return // Non-string shapes get the standard type diagnostics.
	}

	if class != reservedClassNamespace &&
		!strings.HasPrefix(class, reservedClassNamespace+".") {
		return
	}

	r.Errorf("sensitivity class %q is reserved", class).Apply(
		report.Snippet(value),
		report.Snippetf(b.Param.AST(), "parameter declared here"),
		report.Notef("class names beginning with `protowire.` are reserved "+
			"for future spec-defined classes; pick an org-defined class name"),
	)
}

// validateAnnotationUseArg classifies one bound use-site argument
// against its parameter's type.
func validateAnnotationUseArg(
	r *report.Report,
	u AnnotationUse,
	target Annotation,
	b AnnotationArgBinding,
	carrier Type,
) {
	param := b.Param

	// Expression-typed params keep the capture verbatim; the only
	// compile-time obligation is call extraction plus arity
	// verification (RFC-001 §8.1).
	if param.IsExpression() {
		verifyCallArities(r, u, b.Arg)
		return
	}

	value := b.Arg.Value()
	if value.IsZero() {
		r.Errorf("argument %q for `%s` must be a literal or a qualified name",
			param.Name(), target.FullName(),
		).Apply(
			report.Snippet(b.Arg.ValueSpan()),
			report.Snippetf(param.AST(), "parameter declared here"),
			report.Notef("engine-expression fragments are only allowed for `expression`-typed parameters"),
		)
		return
	}

	// Applies to every parameter kind, so it sits outside the switch: the
	// question is only which oneof member the literal lands in, and a
	// declared `string` parameter reaches string_value just as an untyped
	// argument on a `string` carrier does.
	checkStringLiteralUTF8(r, target, param, value, carrier)

	switch {
	case param.IsAny():
		validateAnyArg(r, u, target, param, b.Arg)
		checkCarrierRange(r, target, param, b.Arg.Value(), carrier)
	case param.IsScalar():
		validateScalarArg(r, target, param, value)
	default:
		ut := param.UserType()
		switch {
		case ut.IsZero():
			// Unresolved param type; already diagnosed.
		case ut.IsEnum():
			validateUseEnumArg(r, u, target, param, value, ut)
		default:
			validateMessageArg(r, u, target, param, b.Arg, ut)
		}
	}
}

// lowersToBytesValue reports whether a string literal bound to this
// parameter reaches `bytes_value` rather than `string_value`.
//
// It mirrors the string branch of fdp.buildLiteralArg exactly. Mirroring
// rather than sharing is the risk, as it is for checkCarrierRange;
// TestNonUTF8IsDiagnosedExactlyWhereItReachesStringValue pins the pair
// together.
func lowersToBytesValue(param AnnotationParam, carrier Type) bool {
	if param.IsScalar() {
		return param.Scalar() == predeclared.Bytes
	}
	if !param.IsZero() && !param.IsAny() {
		return false
	}
	// An annotation attached to nothing — a message, a service, a file —
	// has no carrier to consult, so the literal keeps its own spelling and
	// reaches string_value.
	if carrier.IsZero() {
		return false
	}
	return carrier.CarrierScalar() == predeclared.Bytes
}

// checkStringLiteralUTF8 rejects a string literal whose content is not
// valid UTF-8 when it would reach `string_value`.
//
// `string_value` is a proto3 `string`, which cannot hold non-UTF-8 bytes:
// the FileDescriptorProto this compiler emits then fails proto.Marshal
// outright, breaking anything that writes the image rather than only
// annotation-aware readers. #179 closed that for a `bytes` carrier by
// routing to bytes_value; every other carrier still reached string_value
// and still failed to marshal (#184).
//
// Diagnosing rather than re-routing is deliberate. Routing on the
// literal's CONTENT would hand a consumer bytes_value from a parameter
// its own declaration promises is a `string` — the type would depend on
// whether the value happened to be valid UTF-8. There is no valid
// string_value for these bytes, so the source cannot mean what it says,
// and the only question is whether it is reported here or at marshal time
// somewhere else. Carrying arbitrary content is what `bytes` is for.
func checkStringLiteralUTF8(
	r *report.Report,
	target Annotation,
	param AnnotationParam,
	arg ast.ExprAny,
	carrier Type,
) {
	if lowersToBytesValue(param, carrier) {
		return
	}
	checkStringLiteralUTF8Value(r, target, param, arg)
}

func checkStringLiteralUTF8Value(
	r *report.Report,
	target Annotation,
	param AnnotationParam,
	arg ast.ExprAny,
) {
	// A list lowers each element through the same routing, so a bad
	// element is as unmarshallable as a bad argument. Nested lists
	// recurse for the same reason.
	if arg.Kind() == ast.ExprKindArray {
		for elem := range seq.Values(arg.AsArray().Elements()) {
			checkStringLiteralUTF8Value(r, target, param, elem)
		}
		return
	}
	if arg.Kind() != ast.ExprKindLiteral {
		return
	}
	lit := arg.AsLiteral()
	if lit.Token.Kind() != token.String {
		return
	}
	text := lit.Token.AsString().Text()
	if utf8.ValidString(text) {
		return
	}

	r.Errorf("argument %q for `%s` is not valid UTF-8",
		param.Name(), target.FullName(),
	).Apply(
		report.Snippet(arg),
		report.Snippetf(param.AST(), "parameter declared here"),
		report.Notef("it is lowered into `string_value`, a protobuf `string`, "+
			"which cannot carry these bytes — the descriptor would fail to "+
			"serialize"),
		report.Notef("declare the parameter `bytes`, or annotate a `bytes` "+
			"member, to carry arbitrary content"),
	)
}

// validateUseEnumArg checks a use-site argument bound to an enum-
// typed parameter: it must be an identifier path resolving (in the
// use site's scope, in either the value-scoped or enum-qualified
// spelling) to a value of the declared enum.
func validateUseEnumArg(r *report.Report, u AnnotationUse, target Annotation, param AnnotationParam, value ast.ExprAny, enumTy Type) {
	if value.Kind() != ast.ExprKindPath {
		r.Errorf("argument %q for `%s` expects a value of enum `%s`, got %s",
			param.Name(), target.FullName(), enumTy.FullName(), describeArgShape(value),
		).Apply(
			report.Snippet(value),
			report.Snippetf(param.AST(), "parameter declared here"),
		)
		return
	}

	member := u.resolveEnumValue(r, value.AsPath().Path)
	if member.IsZero() {
		return // Already diagnosed.
	}
	if parent := member.Parent(); parent != enumTy {
		r.Errorf("argument %q for `%s` expects a value of enum `%s`, got a value of enum `%s`",
			param.Name(), target.FullName(), enumTy.FullName(), parent.FullName(),
		).Apply(
			report.Snippet(value),
			report.Snippetf(param.AST(), "parameter declared here"),
		)
	}
}

// verifyCallArities checks every extracted function-call site in an
// expression-classified argument against its resolved declaration's
// parameter count. Non-resolving names were never extracted — they
// are presumed engine builtins and stay undiagnosed.
func verifyCallArities(r *report.Report, u AnnotationUse, arg ast.AnnotationUseArg) {
	for _, call := range u.ExtractCalls(arg) {
		declared := call.Target.Params().Len()
		if call.Arity == declared {
			continue
		}
		r.Errorf("call to `%s` has %d argument(s), but the function declares %d",
			call.Target.FullName(), call.Arity, declared,
		).Apply(
			report.Snippet(call.Span),
			report.Snippetf(call.Target.AST().Name(), "declared here"),
		)
	}
}

// validateAnyArg checks an argument bound to an `any`-typed
// parameter: any literal shape is accepted, with the argument's own
// value carrying its typing — enum references must resolve, list
// literals must be homogeneous, and message literals require an
// explicit type name (RFC-001 §5.1 rule 1).
func validateAnyArg(r *report.Report, u AnnotationUse, target Annotation, param AnnotationParam, arg ast.AnnotationUseArg) {
	value := arg.Value()
	switch value.Kind() {
	case ast.ExprKindLiteral, ast.ExprKindPrefixed:
		// Scalar literals carry their own type.

	case ast.ExprKindPath:
		path := value.AsPath().Path
		if name, ok := isSingleIdent(path); ok && (name == "true" || name == "false") {
			return // Boolean literal.
		}
		// Everything else is an enum-value reference and must
		// resolve; this resolution diagnoses failures.
		u.resolveEnumValue(r, path)

	case ast.ExprKindArray:
		validateListLiteralArg(r, u, target, param, value.AsArray())

	case ast.ExprKindDict:
		if arg.MessageType().IsZero() {
			r.Errorf("message-literal argument %q for `%s` requires an explicit type name",
				param.Name(), target.FullName(),
			).Apply(
				report.Snippet(arg.ValueSpan()),
				report.Snippetf(param.AST(), "parameter declared here"),
				report.Notef("the parameter is `any`-typed, so the literal's message type "+
					"cannot come from the declaration; write `SomeType{...}`"),
			)
			return
		}
		validateMessageLiteral(r, u, arg, Type{})
	}
}

// validateMessageArg checks an argument bound to a message-typed
// parameter: the value must be a message literal, and an explicit
// type name (optional here, since the parameter declares the type)
// must resolve to exactly the declared type.
func validateMessageArg(r *report.Report, u AnnotationUse, target Annotation, param AnnotationParam, arg ast.AnnotationUseArg, msgTy Type) {
	value := arg.Value()
	if value.Kind() != ast.ExprKindDict {
		r.Errorf("argument %q for `%s` expects a value of message `%s`, got %s",
			param.Name(), target.FullName(), msgTy.FullName(), describeArgShape(value),
		).Apply(
			report.Snippet(arg.ValueSpan()),
			report.Snippetf(param.AST(), "parameter declared here"),
		)
		return
	}
	if msgTy.IsAny() {
		// A `google.protobuf.Any`-typed parameter follows the same
		// rule as the `any` pseudo-type: the literal's type cannot
		// come from the declaration, so the explicit name is required
		// (RFC-001 §5.1 rule 1).
		if arg.MessageType().IsZero() {
			r.Errorf("message-literal argument %q for `%s` requires an explicit type name",
				param.Name(), target.FullName(),
			).Apply(
				report.Snippet(arg.ValueSpan()),
				report.Snippetf(param.AST(), "parameter declared here"),
				report.Notef("the parameter is `google.protobuf.Any`-typed, so the literal's "+
					"message type cannot come from the declaration; write `SomeType{...}`"),
			)
			return
		}
		msgTy = Type{}
	}
	validateMessageLiteral(r, u, arg, msgTy)
}

// validateMessageLiteral resolves a message literal's explicit type
// name (when present), checks it against the type the surrounding
// context declares (`want`; zero when the context is `any`-typed, in
// which case the caller has enforced that the name is present), and
// evaluates the field initializers against the resolved type via the
// shared option-value evaluator, recording the result for descriptor
// production.
func validateMessageLiteral(r *report.Report, u AnnotationUse, arg ast.AnnotationUseArg, want Type) {
	msgTy := want
	if typeName := arg.MessageType(); !typeName.IsZero() {
		sym := symbolRef{
			File:   u.Context(),
			Report: r,
			scope:  u.scopeName(),
			name:   FullName(typeName.Canonicalized()),
			span:   typeName,
			accept: func(k SymbolKind) bool { return k == SymbolKindMessage },
			want:   taxa.MessageType,
		}.resolve()
		if sym.Kind() != SymbolKindMessage {
			return // Already diagnosed.
		}
		resolved := sym.AsType()
		if !want.IsZero() && resolved != want {
			r.Errorf("message literal is typed `%s`, but the parameter declares `%s`",
				resolved.FullName(), want.FullName(),
			).Apply(report.Snippet(typeName))
			return
		}
		msgTy = resolved
	}
	if msgTy.IsZero() {
		return
	}

	u.evaluateMessageLiteralArg(r, msgTy, arg)
}

// validateListLiteralArg checks a list-literal argument: elements
// must be homogeneous — all of one kind, and for enum-value elements
// all of one enum type (RFC-001 §8.1).
func validateListLiteralArg(r *report.Report, u AnnotationUse, target Annotation, param AnnotationParam, arr ast.ExprArray) {
	var kind string
	var kindSpan ast.ExprAny
	var enumTy Type

	for elem := range seq.Values(arr.Elements()) {
		k, ty := classifyListElement(r, u, target, param, elem)
		if k == "" {
			continue // Already diagnosed.
		}
		if kind == "" {
			kind, kindSpan, enumTy = k, elem, ty
			continue
		}
		if k != kind {
			r.Errorf("heterogeneous list literal in argument %q for `%s`",
				param.Name(), target.FullName(),
			).Apply(
				report.Snippetf(elem, "this element is %s", k),
				report.Snippetf(kindSpan, "but this element is %s", kind),
				report.Notef("all elements of a list literal must be the same kind"),
			)
			continue
		}
		if !ty.IsZero() && !enumTy.IsZero() && ty != enumTy {
			r.Errorf("mixed enum types in list literal: `%s` and `%s`",
				enumTy.FullName(), ty.FullName(),
			).Apply(
				report.Snippet(elem),
				report.Snippetf(kindSpan, "first element's enum type is `%s`", enumTy.FullName()),
			)
		}
	}
}

// classifyListElement determines a list element's homogeneity kind
// and, for enum-value references, its resolved enum type. Returns
// "" when the element failed to resolve (diagnosed here).
func classifyListElement(r *report.Report, u AnnotationUse, target Annotation, param AnnotationParam, elem ast.ExprAny) (string, Type) {
	switch elem.Kind() {
	case ast.ExprKindLiteral:
		if elem.AsLiteral().Token.Kind() == token.String {
			return "a string", Type{}
		}
		return "a number", Type{}
	case ast.ExprKindPrefixed:
		return "a number", Type{}
	case ast.ExprKindPath:
		path := elem.AsPath().Path
		if name, ok := isSingleIdent(path); ok && (name == "true" || name == "false") {
			return "a boolean", Type{}
		}
		member := u.resolveEnumValue(r, path)
		if member.IsZero() {
			return "", Type{}
		}
		return "an enum value", member.Parent()
	case ast.ExprKindArray:
		validateListLiteralArg(r, u, target, param, elem.AsArray())
		return "a list", Type{}
	case ast.ExprKindDict:
		// Message-literal elements follow the same explicit-typing
		// rule as argument-level literals under an `any`-typed
		// context (RFC-001 §5.1 rule 1): the element's message type
		// cannot come from the declaration, so the literal must name
		// it. The parser's classifier carries the name on the dict
		// itself — see [ast.ExprDict.TypeName].
		dict := elem.AsDict()
		typeName := dict.TypeName()
		if typeName.IsZero() {
			r.Errorf("message-literal list element in argument %q for `%s` requires an explicit type name",
				param.Name(), target.FullName(),
			).Apply(
				report.Snippet(elem),
				report.Snippetf(param.AST(), "parameter declared here"),
				report.Notef("the enclosing context is `any`-typed, so the element's message type "+
					"cannot come from the declaration; write `SomeType{...}`"),
			)
			return "", Type{}
		}
		sym := symbolRef{
			File:   u.Context(),
			Report: r,
			scope:  u.scopeName(),
			name:   FullName(typeName.Canonicalized()),
			span:   typeName,
			accept: func(k SymbolKind) bool { return k == SymbolKindMessage },
			want:   taxa.MessageType,
		}.resolve()
		if sym.Kind() != SymbolKindMessage {
			return "", Type{} // Already diagnosed.
		}
		u.evaluateMessageLiteralElem(r, sym.AsType(), dict)
		return "a message", Type{}
	}
	return "", Type{}
}

// describeArgShape renders an argument value's shape for mismatch
// diagnostics.
func describeArgShape(value ast.ExprAny) string {
	switch value.Kind() {
	case ast.ExprKindLiteral:
		return "a literal"
	case ast.ExprKindPrefixed:
		return "a prefixed expression"
	case ast.ExprKindPath:
		return "an identifier reference"
	case ast.ExprKindArray:
		return "a list literal"
	case ast.ExprKindDict:
		return "a message literal"
	default:
		return "this expression shape"
	}
}

// allAnnotationUses yields every materialised [AnnotationUse] in the
// file by walking every carrier. The walk order matches
// [resolveAnnotationUses].
func allAnnotationUses(file *File) func(yield func(AnnotationUse, Type) bool) {
	return func(yield func(AnnotationUse, Type) bool) {
		// carrier is the element type of the MEMBER the annotation is
		// attached to. A message, a service or a declaration is not a
		// member and passes the zero Type, which is distinct from a member
		// whose type simply has no scalar (an unmapped message): the first
		// has nothing to bound against, the second lowers as int_value and
		// is bounded by that.
		//
		// An ENUM VALUE is a member with no element type at all, so it
		// passes the zero Type through Element() and lands in the first
		// case — correctly, since there is no more of a type to bound
		// against there than on the enum itself.
		emit := func(uses seq.Indexer[AnnotationUse], carrier Type) bool {
			for u := range seq.Values(uses) {
				if !yield(u, carrier) {
					return false
				}
			}
			return true
		}
		for ty := range seq.Values(file.AllTypes()) {
			// A synthesized map entry reports the annotations of the field
			// that produced it, so walking it yields every map-field
			// annotation a second time — with the zero Type, because an
			// entry message is not a member. Nothing an author wrote lives
			// on an entry message: `@note` on `map<string, int32> f` was
			// written on `f`, and is already emitted above through it.
			//
			// Harmless while the only zero-carrier check returned
			// immediately; checkStringLiteralUTF8 is the first that does
			// not, and saw a `map<string, bytes>` argument as though it
			// were annotating a message.
			if ty.IsMapEntry() {
				continue
			}
			if !emit(ty.Annotations(), Type{}) {
				return
			}
			for field := range seq.Values(ty.Members()) {
				if !emit(field.Annotations(), field.Element()) {
					return
				}
			}
			for o := range seq.Values(ty.Oneofs()) {
				if !emit(o.Annotations(), Type{}) {
					return
				}
			}
		}
		for ext := range seq.Values(file.AllExtensions()) {
			if !emit(ext.Annotations(), ext.Element()) {
				return
			}
		}
		for svc := range seq.Values(file.Services()) {
			if !emit(svc.Annotations(), Type{}) {
				return
			}
			for m := range seq.Values(svc.Methods()) {
				if !emit(m.Annotations(), Type{}) {
					return
				}
			}
		}
		for ann := range seq.Values(file.Annotations()) {
			if !emit(ann.Annotations(), Type{}) {
				return
			}
		}
	}
}

// validateAnnotationArg type-checks a default-value expression
// against the corresponding parameter. Diagnostics are point-
// accurate to the expression span. (Use-site arguments go through
// [validateAnnotationUseArg] instead, which handles the capture-
// then-classify argument model.)
func validateAnnotationArg(r *report.Report, target Annotation, param AnnotationParam, arg ast.ExprAny) {
	// The `expression` pseudo-type accepts any default shape. Same
	// for fully-unresolved params (a diagnostic was already emitted).
	if param.IsExpression() {
		return
	}
	if param.IsAny() {
		// Identifier defaults on `any` params are enum-value
		// references (the boolean keywords aside) and must resolve —
		// the carrier lowers them resolved (RFC-001 §8.1).
		if arg.Kind() == ast.ExprKindPath {
			path := arg.AsPath().Path
			if name, ok := isSingleIdent(path); ok && (name == "true" || name == "false") {
				return
			}
			resolveEnumValueRef(param.Context(), r, target.FullName().Parent(), path)
		}
		return
	}
	if param.IsScalar() {
		validateScalarArg(r, target, param, arg)
		return
	}
	if ut := param.UserType(); !ut.IsZero() && ut.IsEnum() {
		validateEnumArg(r, target.FullName().Parent(), target, param, arg, ut)
		return
	}
	// Message-typed defaults remain unchecked; they pair with
	// message-literal lowering.
}

// validateEnumArg checks that `arg` is an identifier path resolving
// to an enum value of `enumTy`, relative to scope. Anything else —
// a literal, a prefixed expression, or a path that resolves to a
// different symbol or the wrong enum — produces a diagnostic.
func validateEnumArg(r *report.Report, scope FullName, target Annotation, param AnnotationParam, arg ast.ExprAny, enumTy Type) {
	mismatch := func(actual string) {
		r.Errorf("argument %q for `%s` expects a value of enum `%s`, got %s",
			param.Name(), target.FullName(), enumTy.FullName(), actual,
		).Apply(
			report.Snippet(arg),
			report.Snippetf(param.AST(), "parameter declared here"),
		)
	}

	if arg.Kind() != ast.ExprKindPath {
		switch arg.Kind() {
		case ast.ExprKindLiteral:
			mismatch("a literal")
		case ast.ExprKindPrefixed:
			mismatch("a prefixed expression")
		default:
			mismatch("this expression shape")
		}
		return
	}

	path := arg.AsPath().Path
	member := resolveEnumValueRef(param.Context(), r, scope, path)
	if member.IsZero() {
		return // resolve already emitted a diagnostic
	}
	if parent := member.Parent(); parent != enumTy {
		r.Errorf("argument %q for `%s` expects a value of enum `%s`, got a value of enum `%s`",
			param.Name(), target.FullName(), enumTy.FullName(), parent.FullName(),
		).Apply(
			report.Snippet(arg),
			report.Snippetf(param.AST(), "parameter declared here"),
		)
	}
}

// validateScalarArg checks one argument against a predeclared scalar
// parameter. Diagnostics use the parameter's TypeName for the
// "expected X" portion.
func validateScalarArg(r *report.Report, target Annotation, param AnnotationParam, arg ast.ExprAny) {
	scalar := param.Scalar()

	mismatch := func(actual string) {
		r.Errorf("argument %q for `%s` expects %s, got %s",
			param.Name(), target.FullName(), param.TypeName(), actual,
		).Apply(
			report.Snippet(arg),
			report.Snippetf(param.AST(), "parameter declared here"),
		)
	}

	switch arg.Kind() {
	case ast.ExprKindLiteral:
		lit := arg.AsLiteral()
		switch lit.Token.Kind() {
		case token.String:
			if scalar == predeclared.String || scalar == predeclared.Bytes {
				return
			}
			mismatch("string literal")
		case token.Number:
			if isNumericScalar(scalar) {
				checkIntegerRange(r, target, param, arg, scalar, false, lit.Token.AsNumber())
				return
			}
			mismatch("number literal")
		default:
			mismatch("literal")
		}

	case ast.ExprKindPath:
		path := arg.AsPath().Path
		if name, ok := isSingleIdent(path); ok &&
			scalar == predeclared.Bool && (name == "true" || name == "false") {
			return
		}
		mismatch("identifier reference")

	case ast.ExprKindPrefixed:
		pref := arg.AsPrefixed()
		inner := pref.Expr()
		if pref.Prefix() == keyword.Sub &&
			inner.Kind() == ast.ExprKindLiteral &&
			inner.AsLiteral().Token.Kind() == token.Number &&
			isNumericScalar(scalar) {
			checkIntegerRange(r, target, param, arg, scalar, true, inner.AsLiteral().Token.AsNumber())
			return
		}
		mismatch("prefixed expression")

	default:
		// Every remaining shape — a list literal, a message literal — is
		// not a scalar, and `repeated` is not a spellable parameter type
		// (classifyAnnotationParamType: "annotation parameter type must be
		// a name"), so a list can never be valid on one. The sibling
		// validators reject these shapes already — validateEnumArg via
		// describeArgShape, validateMessageArg for anything that is not a
		// dict — but a scalar parameter fell through this switch silently,
		// so `@a([1e100, 5])` on an `int32` parameter compiled and reached
		// the carrier as a list, walking around the range check above with
		// one pair of brackets.
		mismatch(describeArgShape(arg))
	}
}

// isNumericScalar reports whether `n` is one of the predeclared
// numeric scalars (excludes Bool, String, Bytes, and the
// `Inf`/`NaN`/`True`/`False` keyword values).
func isNumericScalar(n predeclared.Name) bool {
	switch n {
	case predeclared.Int32, predeclared.Int64,
		predeclared.UInt32, predeclared.UInt64,
		predeclared.SInt32, predeclared.SInt64,
		predeclared.Fixed32, predeclared.Fixed64,
		predeclared.SFixed32, predeclared.SFixed64,
		predeclared.Float, predeclared.Double:
		return true
	}
	return false
}

// integerRange gives the inclusive bounds of a predeclared integer scalar as
// magnitudes: the ceiling for a non-negative literal, and the largest
// magnitude a negative one may have. Reports false for a non-integer scalar.
//
// Negative bounds are magnitudes because a literal carries its sign as a
// separate prefix node, so the value reaching here is always unsigned.
// int32's floor is -2^31, whose magnitude is one past its ceiling.
func integerRange(n predeclared.Name) (limit, limitNegative uint64, signed, ok bool) {
	switch n {
	case predeclared.Int32, predeclared.SInt32, predeclared.SFixed32:
		return math.MaxInt32, 1 << 31, true, true
	case predeclared.Int64, predeclared.SInt64, predeclared.SFixed64:
		return math.MaxInt64, 1 << 63, true, true
	case predeclared.UInt32, predeclared.Fixed32:
		return math.MaxUint32, 0, false, true
	case predeclared.UInt64, predeclared.Fixed64:
		return math.MaxUint64, 0, false, true
	}
	return 0, 0, false, false
}

// rangeFault is what an integer bound found wrong with a literal, if
// anything. Separated from the diagnostic so the parameter bound and the
// carrier bound can share the decision while wording it differently.
type rangeFault int

const (
	rangeOK rangeFault = iota
	rangeTooLarge
	rangeNegativeOnUnsigned
)

// integerRangeFault reports whether a numeric literal fits an integer
// scalar. negative says the literal carried a `-` prefix; the magnitude
// reaching here is always unsigned.
func integerRangeFault(scalar predeclared.Name, negative bool, num token.NumberToken) rangeFault {
	limit, limitNegative, signed, ok := integerRange(scalar)
	if !ok {
		return rangeOK // float or double; nothing to bound.
	}

	v, exact := num.Int()
	if !exact {
		// Int saturates rather than failing, so an inexact conversion is
		// either a value past uint64 or a fraction. Tell them apart by
		// whether the value is whole, NOT by magnitude: float64 rounds
		// MaxUint64 and MaxUint64+1 to the same number, so comparing
		// against the bound misses the literal one past it.
		if f, _ := num.Float(); f == math.Trunc(f) {
			return rangeTooLarge
		}
		// A fraction is a different question and still lowers, truncated
		// (#165) — but Int has already truncated it, so `v` is exactly the
		// magnitude that reaches the carrier. Bound that, rather than
		// returning: a fraction is not a licence to skip the check, and
		// returning here let `@a(99999999999.5)` past an `int32` parameter
		// and `@a(-1.5)` past an unsigned one, which are the very values
		// this function exists to reject.
	}

	if negative {
		// Any negated literal on an unsigned type is an error, whatever its
		// magnitude — `-0` included. That is what `checkIntBounds`
		// (ir/lower_eval.go) does for an unsigned FIELD, and the two
		// checkers answering differently inside one package was the real
		// defect (#169).
		//
		// A signed type still takes `-0` and `-0.4`: they reach the range
		// check below with a magnitude of zero, which fits.
		if !signed {
			return rangeNegativeOnUnsigned
		}
		if v > limitNegative {
			return rangeTooLarge
		}
		return rangeOK
	}
	if v > limit {
		return rangeTooLarge
	}
	return rangeOK
}

// checkIntegerRange diagnoses a numeric literal that cannot be represented by
// the integer scalar its PARAMETER declares.
//
// Without it the value was carried into the carrier however it happened to
// convert: `1e100` saturated to MaxUint64 and reinterpreted to -1, an
// unsigned parameter accepted a negative literal, and a value past the
// declared width simply wrapped — none of them diagnosed, and none
// recoverable by a consumer reading the carrier.
//
// A fractional literal in range is deliberately NOT diagnosed here. It is a
// different question — the value fits, it is just not a whole number — and
// it is pinned in fdp's routing table rather than decided here (#165).
func checkIntegerRange(
	r *report.Report,
	target Annotation,
	param AnnotationParam,
	arg ast.ExprAny,
	scalar predeclared.Name,
	negative bool,
	num token.NumberToken,
) {
	switch integerRangeFault(scalar, negative, num) {
	case rangeTooLarge:
		r.Errorf("argument %q for `%s` is out of range for `%s`",
			param.Name(), target.FullName(), param.TypeName(),
		).Apply(
			report.Snippet(arg),
			report.Snippetf(param.AST(), "parameter declared here"),
		)
	case rangeNegativeOnUnsigned:
		r.Errorf("argument %q for `%s` is negative, but `%s` is unsigned",
			param.Name(), target.FullName(), param.TypeName(),
		).Apply(
			report.Snippet(arg),
			report.Snippetf(param.AST(), "parameter declared here"),
		)
	case rangeOK:
	}
}

// checkCarrierRange diagnoses a numeric literal that the thing the
// annotation is attached to cannot hold.
//
// An untyped parameter says nothing about the value, so until #172 nothing
// bounded it at the use site. The carrier does say something: a literal in
// (MaxInt64, MaxUint64] lowers into `int_value`, an int64, so on a SIGNED
// 64-bit carrier it wraps to a negative — and lands inside the type's
// range, which is why nothing downstream caught it either. The 32-bit
// carriers were reported only because the wrapped value still did not fit
// them (#177).
//
// An unsigned carrier holds the value and recovers it from its own type,
// so it is not bounded. A float carrier is routed to `double_value`
// instead and never reaches int_value at all.
//
// A carrier with no scalar — an unmapped message such as `pxf.BigInt`, or
// an annotation on a message or service — is bounded as int64, because
// int64 is what its literal will lower into. That is what makes
// `@default(1e19)` on a `pxf.BigInt` an error rather than a silently
// negative value (#176).
//
// # What this bound assumes, and what it costs
//
// It reads every untyped argument as a value FOR the annotated field.
// That holds for `@default` and `@example`, whose whole meaning is a value
// for the thing they sit on, and not for an annotation whose argument is
// about something else:
//
//	int32 f = 1 @max_bytes(5000000000);   // rejected
//
// `@max_bytes` is a byte limit, not a value for an int32 field, and the
// bound rejects it anyway. That is a false positive and it is deliberate
// (#183): a wrapped value a consumer cannot distinguish from a deliberate
// one is the worse failure, and narrowing the bound to value-carrying
// annotations would need the compiler to know which annotations those are
// — a spec-level marker or a hardcoded list, neither of which exists.
//
// Note the asymmetry that makes the trade acceptable: carrier-directed
// ROUTING is harmless when the assumption is wrong, because the value
// survives in a different member. Only BOUNDING rejects, so only bounding
// carries this cost.
//
// # Enum carriers are bounded at int64, not int32
//
// An enum value is an int32, so `E f = 1 @default(5000000000)` is rejected
// downstream while compiling clean here — the same "diagnosed downstream
// rather than at compile" case this function closes for `int32` and
// friends. It is left that way on purpose (#183): an enum field is the
// carrier most likely to hold an argument that is not a value for it, so
// tightening it concentrates the false positive above rather than spending
// it where a value is actually meant.
func checkCarrierRange(
	r *report.Report,
	target Annotation,
	param AnnotationParam,
	arg ast.ExprAny,
	carrier Type,
) {
	if carrier.IsZero() {
		// Not attached to a member — a message, a service, a declaration.
		// There is no type to bound against, and the literal's ambiguity
		// there is the documented limit of carrier routing (#172).
		return
	}

	// `describe` names what the literal has to fit, in the reader's terms,
	// and is phrased so it reads as the subject of "… is unsigned" as well
	// as the object of "out of range for …".
	//
	// With a scalar carrier that is the annotated type. A wrapper names
	// both, because the annotated type is the wrapper and the bound is the
	// scalar it wraps — calling `int64` "the annotated type" of a
	// `google.protobuf.Int64Value` field would be a lie. With no scalar at
	// all it is int_value itself, because that is what the literal becomes
	// and the annotated type is a message.
	bound := carrier.CarrierScalar()
	describe := fmt.Sprintf("the annotated type `%s`", bound)
	switch {
	case bound == predeclared.Unknown:
		bound = predeclared.Int64
		describe = "the 64-bit signed `int_value` an untyped argument " +
			"on this carrier is lowered into"
	case carrier.IsMapEntry():
		// The entry message is synthesized; naming it would point at
		// something the author never wrote.
		describe = fmt.Sprintf("`%s`, the map's value type", bound)
	case carrier.Predeclared() == predeclared.Unknown:
		describe = fmt.Sprintf("the scalar `%s` wrapped by the annotated type `%s`",
			bound, carrier.FullName())
	}
	if bound == predeclared.Double {
		// double_value IS a double, so there is nothing it cannot hold.
		// `float` is not exempt: see the float32 case in
		// checkCarrierRangeValue.
		return
	}

	checkCarrierRangeValue(r, target, param, arg, bound, describe)
}

// checkCarrierRangeValue applies a resolved carrier bound to one argument
// value expression.
//
// A list argument lowers element by element through the SAME carrier
// routing the scalar form uses — fdp's buildListLiteral hands each element
// to buildArgValue with the argument's carrier — so `@default([1e19])`
// wraps on an `int64` carrier exactly as `@default(1e19)` does. Bounding
// the argument without descending into it left #177 fixed in one shape and
// open in the other; nesting recurses for the same reason, since
// buildListElement lowers a nested list through buildArgValue too.
func checkCarrierRangeValue(
	r *report.Report,
	target Annotation,
	param AnnotationParam,
	arg ast.ExprAny,
	bound predeclared.Name,
	describe string,
) {
	if arg.Kind() == ast.ExprKindArray {
		for elem := range seq.Values(arg.AsArray().Elements()) {
			checkCarrierRangeValue(r, target, param, elem, bound, describe)
		}
		return
	}

	negative := false
	value := arg
	if arg.Kind() == ast.ExprKindPrefixed {
		pref := arg.AsPrefixed()
		if pref.Prefix() != keyword.Sub {
			return
		}
		negative = true
		value = pref.Expr()
	}
	if value.Kind() != ast.ExprKindLiteral {
		return
	}
	lit := value.AsLiteral()
	if lit.Token.Kind() != token.Number {
		return
	}

	num0 := lit.Token.AsNumber()

	// A float carrier is bounded by its OWN width, not by int_value's: its
	// literal is routed to `double_value` whatever the spelling, so the
	// int-route guards below never apply and this has to come first.
	//
	// double_value holds values float32 cannot, and assigning one yields
	// +Inf — indistinguishable from a literal that meant infinity (#180).
	if bound == predeclared.Float {
		v, _ := num0.Float()
		// Ask the conversion rather than comparing against MaxFloat32:
		// 3.4028235e38 is the canonical spelling of the largest float and
		// is strictly GREATER than its exact binary value, so a comparison
		// rejects it while the conversion rounds it down. Only values that
		// round to infinity are out of range.
		if !math.IsInf(v, 0) && math.IsInf(float64(float32(v)), 0) {
			r.Errorf("argument %q for `%s` is out of range for %s",
				param.Name(), target.FullName(), describe,
			).Apply(
				report.Snippet(arg),
				report.Notef("`float` holds up to about 3.4e38; a larger value "+
					"reaches a consumer as infinity"),
			)
		}
		return
	}

	// Bound only what actually lands in `int_value`. A float-spelled
	// literal, and one that does not fit uint64, are routed to
	// `double_value` instead (#149, #165) — and so is a NEGATIVE literal
	// whose magnitude exceeds int64, because negating a reinterpreted
	// magnitude would flip the sign back. Bounding those would reject
	// values that lower correctly.
	//
	// Mirroring the lowering rather than restating it is the risk here: if
	// buildLiteralArg's routing changes, this guard has to change with it.
	// TestCarrierBoundOnlyRejectsWhatLowersAsInt pins the pair together.
	num := lit.Token.AsNumber()
	if num.IsFloat() {
		return
	}
	magnitude, exact := num.Int()
	if !exact {
		return
	}
	if negative && magnitude > 1<<63 {
		return
	}

	switch integerRangeFault(bound, negative, num) {
	case rangeTooLarge:
		r.Errorf("argument %q for `%s` is out of range for %s",
			param.Name(), target.FullName(), describe,
		).Apply(
			report.Snippet(arg),
			// Both halves of the bound are stated, because only the first
			// is a wrap: above MaxInt64 the value overflows `int_value`
			// itself and reaches a consumer negated, while at or below it
			// the value survives the carrier intact and it is the
			// annotated type that cannot hold it.
			report.Notef("an untyped annotation argument is carried as `int_value`, "+
				"a 64-bit signed integer: past its range the value reaches a consumer "+
				"wrapped, and within it the value survives but the annotated type "+
				"still cannot hold it"),
		)
	case rangeNegativeOnUnsigned:
		r.Errorf("argument %q for `%s` is negative, but %s is unsigned",
			param.Name(), target.FullName(), describe,
		).Apply(report.Snippet(arg))
	case rangeOK:
	}
}
