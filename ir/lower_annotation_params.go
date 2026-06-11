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
// list against its target [Annotation]'s parameter signature.
//
// Phase B3 validation. Runs after [resolveAnnotationParamTypes].
//
// Checks performed today:
//
//   - Arity: argument count must not exceed parameter count. (Too
//     few is allowed when the missing params have defaults — but
//     default-expression lowering is itself B3-deferred, so for now
//     we only flag arg overflow.)
//   - Per-arg type compatibility:
//   - Predeclared scalars: each scalar kind matches the
//     corresponding literal kind (string→string, integer→
//     number, bool→`true`/`false`).
//   - `expression` / `any`: accept any argument shape the
//     parser's narrow legalize pass already passes through.
//   - User type (message/enum): not type-checked here; arguments
//     for user-typed params would need separate symbol
//     resolution which sits next to the larger options-style
//     resolution stack.
func validateAnnotationUseArgs(file *File, r *report.Report) {
	for u := range allAnnotationUses(file) {
		target := u.Target()
		if target.IsZero() {
			continue // Already diagnosed in B2.
		}

		params := target.Params()
		args := u.AST().Args()
		if args.Len() > params.Len() {
			r.Errorf("too many arguments for `%s`: got %d, want at most %d",
				target.FullName(), args.Len(), params.Len(),
			).Apply(
				report.Snippet(u.AST()),
				report.Snippetf(target.AST().Name(), "declared here"),
			)
		}

		for i := range args.Len() {
			if i >= params.Len() {
				break
			}
			validateAnnotationArg(r, target, params.At(i), args.At(i))
		}
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

// validateAnnotationArg type-checks one argument against the
// corresponding parameter. Diagnostics are point-accurate to the
// argument span.
func validateAnnotationArg(r *report.Report, target Annotation, param AnnotationParam, arg ast.ExprAny) {
	// Pseudo-types `expression` and `any` accept any arg shape the
	// parser legalize pass already let through. Same for fully-
	// unresolved params (a diagnostic was already emitted).
	if param.IsExpression() || param.IsAny() {
		return
	}
	if param.IsScalar() {
		validateScalarArg(r, target, param, arg)
		return
	}
	// User-type params: argument type-check requires options-style
	// resolution (path → enum value, message literal handling) which
	// the narrow legalize pass intentionally rejects today. Leave
	// these unchecked until the options-style path is wired in.
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
