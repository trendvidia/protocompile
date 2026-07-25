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
	"strings"

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
	for u := range allAnnotationUses(file) {
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
				validateAnnotationUseArg(r, u, target, b)
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

	value := b.Arg.Value()
	if value.Kind() != ast.ExprKindLiteral {
		return // Non-string shapes get the standard type diagnostics.
	}
	tok := value.AsLiteral().Token
	if tok.Kind() != token.String {
		return
	}

	class := tok.AsString().Text()
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
func validateAnnotationUseArg(r *report.Report, u AnnotationUse, target Annotation, b AnnotationArgBinding) {
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

	switch {
	case param.IsAny():
		validateAnyArg(r, u, target, param, b.Arg)
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
func allAnnotationUses(file *File) func(yield func(AnnotationUse) bool) {
	return func(yield func(AnnotationUse) bool) {
		emit := func(uses seq.Indexer[AnnotationUse]) bool {
			for u := range seq.Values(uses) {
				if !yield(u) {
					return false
				}
			}
			return true
		}
		for ty := range seq.Values(file.AllTypes()) {
			if !emit(ty.Annotations()) {
				return
			}
			for field := range seq.Values(ty.Members()) {
				if !emit(field.Annotations()) {
					return
				}
			}
			for o := range seq.Values(ty.Oneofs()) {
				if !emit(o.Annotations()) {
					return
				}
			}
		}
		for ext := range seq.Values(file.AllExtensions()) {
			if !emit(ext.Annotations()) {
				return
			}
		}
		for svc := range seq.Values(file.Services()) {
			if !emit(svc.Annotations()) {
				return
			}
			for m := range seq.Values(svc.Methods()) {
				if !emit(m.Annotations()) {
					return
				}
			}
		}
		for ann := range seq.Values(file.Annotations()) {
			if !emit(ann.Annotations()) {
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
			return
		}
		mismatch("prefixed expression")
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
