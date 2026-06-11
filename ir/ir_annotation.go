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
	"github.com/trendvidia/protocompile/id"
	"github.com/trendvidia/protocompile/internal/intern"
	"github.com/trendvidia/protocompile/seq"
)

// Annotation is an `annotation` declaration introduced by the
// Protowire Schema Extensions (PSE) grammar — see
// [ast.DeclAnnotation].
//
// Annotation declarations name a metadata attachment that use sites
// reference via `@name(args)` syntax (see [ast.DeclAnnotationUse]).
// They live in the file's exported symbol table under
// [SymbolKindAnnotation] so cross-file resolution works the same way
// it does for `message` and `service`.
//
// This is Phase B1 scope: symbol registration only. Subsequent phases
// will add use-site name resolution, argument type-checking against
// the parameter signature, and FDP emission via the universal
// annotation carrier extension.
type Annotation id.Node[Annotation, *File, *rawAnnotation]

// AnnotationParam is a single parameter of an [Annotation]
// declaration — see [ast.DeclAnnotationParam].
//
// Parameters carry a name, a declared type, and an optional default
// expression. Phase B1 stores the AST link and the parameter name;
// type-resolution into [TypeAny] and default-value lowering land in
// later phases together with use-site type-checking.
type AnnotationParam id.Node[AnnotationParam, *File, *rawAnnotationParam]

// AnnotationUse is a single `@name(args)` annotation use site — see
// [ast.DeclAnnotationUse]. Use sites are attached to a carrier
// declaration via that carrier's `Annotations()` accessor (e.g.
// [Type.Annotations], [Member.Annotations], [Service.Annotations]).
//
// As of Phase B2, a use site carries its source AST link plus the
// resolved [Annotation] (or a zero [Ref] when name resolution failed
// — the diagnostic is emitted at resolve time). The argument list is
// not yet materialised into IR values: that pairs with the
// signature-aware type-check in Phase B3.
type AnnotationUse id.Node[AnnotationUse, *File, *rawAnnotationUse]

type rawAnnotation struct {
	def       id.ID[ast.DeclAnnotation]
	fqn, name intern.ID
	params    []id.ID[AnnotationParam]

	annotationUses []id.ID[AnnotationUse]
}

type rawAnnotationParam struct {
	def      id.ID[ast.DeclAnnotationParam]
	parent   id.ID[Annotation]
	name     intern.ID
	typeName intern.ID // The interned textual type name, e.g. "string", "expression", or "myco.Foo". Used for diagnostics; always populated when the param has a type annotation.

	// Exactly one of the following classifies the parameter's type;
	// resolved by [resolveAnnotationParamTypes]. The zero state
	// (`scalar == predeclared.Unknown && !isExpression && !isAny &&
	// userType.IsZero()`) means the type didn't resolve to anything
	// recognised — the resolution pass already emitted a diagnostic.
	scalar       predeclared.Name
	isExpression bool
	isAny        bool
	userType     Ref[Symbol]
}

type rawAnnotationUse struct {
	def    id.ID[ast.DeclAnnotationUse]
	target Ref[Symbol]
}

// AST returns the declaration for this annotation, if known.
func (a Annotation) AST() ast.DeclAnnotation {
	if a.IsZero() {
		return ast.DeclAnnotation{}
	}
	return id.Wrap(a.Context().AST(), a.Raw().def)
}

// Name returns this annotation's declared name, i.e. the last
// component of its full name.
func (a Annotation) Name() string {
	return a.FullName().Name()
}

// FullName returns this annotation's fully-qualified name.
func (a Annotation) FullName() FullName {
	if a.IsZero() {
		return ""
	}
	return FullName(a.Context().session.intern.Value(a.Raw().fqn))
}

// InternedName returns the intern ID for [Annotation.FullName]().Name().
func (a Annotation) InternedName() intern.ID {
	if a.IsZero() {
		return 0
	}
	return a.Raw().name
}

// InternedFullName returns the intern ID for [Annotation.FullName].
func (a Annotation) InternedFullName() intern.ID {
	if a.IsZero() {
		return 0
	}
	return a.Raw().fqn
}

// Params returns the parameters of this annotation declaration.
func (a Annotation) Params() seq.Indexer[AnnotationParam] {
	var params []id.ID[AnnotationParam]
	if !a.IsZero() {
		params = a.Raw().params
	}

	return seq.NewFixedSlice(
		params,
		func(_ int, p id.ID[AnnotationParam]) AnnotationParam {
			return id.Wrap(a.Context(), p)
		},
	)
}

// Annotations returns the annotation use sites attached to this
// declaration (trailing form: `annotation foo @bar @baz;`). See
// [Type.Annotations] for the resolution model.
func (a Annotation) Annotations() seq.Indexer[AnnotationUse] {
	if a.IsZero() {
		return annotationUses(nil, nil)
	}
	return annotationUses(a.Context(), a.Raw().annotationUses)
}

// AST returns the declaration for this annotation parameter, if known.
func (p AnnotationParam) AST() ast.DeclAnnotationParam {
	if p.IsZero() {
		return ast.DeclAnnotationParam{}
	}
	return id.Wrap(p.Context().AST(), p.Raw().def)
}

// Name returns this parameter's declared name.
func (p AnnotationParam) Name() string {
	if p.IsZero() {
		return ""
	}
	return p.Context().session.intern.Value(p.Raw().name)
}

// InternedName returns the intern ID for [AnnotationParam.Name].
func (p AnnotationParam) InternedName() intern.ID {
	if p.IsZero() {
		return 0
	}
	return p.Raw().name
}

// Annotation returns the annotation declaration that this parameter
// belongs to.
func (p AnnotationParam) Annotation() Annotation {
	if p.IsZero() {
		return Annotation{}
	}
	return id.Wrap(p.Context(), p.Raw().parent)
}

// TypeName returns the literal text of the parameter's declared
// type, e.g. "string", "expression", or "myco.SomeMessage". Useful
// for diagnostics — see [AnnotationParam.IsScalar],
// [AnnotationParam.IsExpression], [AnnotationParam.IsAny], and
// [AnnotationParam.UserType] for the resolved classification.
func (p AnnotationParam) TypeName() string {
	if p.IsZero() {
		return ""
	}
	if p.Raw().typeName == 0 {
		return ""
	}
	return p.Context().session.intern.Value(p.Raw().typeName)
}

// Scalar returns the predeclared scalar type for this parameter, or
// [predeclared.Unknown] when the parameter's type is not a scalar.
// Pair with [AnnotationParam.IsScalar] to disambiguate "type
// resolved to a non-scalar" from "predeclared.Unknown is the type".
func (p AnnotationParam) Scalar() predeclared.Name {
	if p.IsZero() {
		return predeclared.Unknown
	}
	return p.Raw().scalar
}

// IsScalar reports whether the parameter's declared type resolved
// to a predeclared scalar (string, int32, etc.).
func (p AnnotationParam) IsScalar() bool {
	return !p.IsZero() && p.Raw().scalar != predeclared.Unknown
}

// IsExpression reports whether the parameter's declared type is the
// special PSE pseudo-type `expression` (an opaque text payload
// validated by the configured engine at run time, not by
// protocompile).
func (p AnnotationParam) IsExpression() bool {
	return !p.IsZero() && p.Raw().isExpression
}

// IsAny reports whether the parameter's declared type is the special
// PSE pseudo-type `any` (accepts any literal-shaped argument).
func (p AnnotationParam) IsAny() bool {
	return !p.IsZero() && p.Raw().isAny
}

// UserType returns the resolved [Type] when the parameter's
// declared type is a user-defined message or enum. Returns a zero
// [Type] for predeclared scalars, the `expression`/`any` pseudo-
// types, and unresolved references.
func (p AnnotationParam) UserType() Type {
	if p.IsZero() {
		return Type{}
	}
	sym := GetRef(p.Context(), p.Raw().userType)
	switch sym.Kind() {
	case SymbolKindMessage, SymbolKindEnum:
		return sym.AsType()
	}
	return Type{}
}

// AST returns the use-site syntax for this annotation use.
func (u AnnotationUse) AST() ast.DeclAnnotationUse {
	if u.IsZero() {
		return ast.DeclAnnotationUse{}
	}
	return id.Wrap(u.Context().AST(), u.Raw().def)
}

// Target returns the [Annotation] this use site resolves to. Returns
// a zero [Annotation] when name resolution failed (in which case the
// resolve pass already emitted a diagnostic).
func (u AnnotationUse) Target() Annotation {
	if u.IsZero() {
		return Annotation{}
	}
	sym := GetRef(u.Context(), u.Raw().target)
	if sym.Kind() != SymbolKindAnnotation {
		return Annotation{}
	}
	return sym.AsAnnotation()
}

// TargetRef returns the raw [Ref] for the resolved target symbol.
// Useful for callers that want to detect resolution failures (zero
// ref) without going through the [Annotation] accessor.
func (u AnnotationUse) TargetRef() Ref[Symbol] {
	if u.IsZero() {
		return Ref[Symbol]{}
	}
	return u.Raw().target
}

// annotationUses converts a slice of [AnnotationUse] IDs on a carrier
// into a [seq.Indexer] for export. It's a tiny helper because every
// carrier type expresses its `.Annotations()` accessor the same way.
func annotationUses(f *File, ids []id.ID[AnnotationUse]) seq.Indexer[AnnotationUse] {
	return seq.NewFixedSlice(
		ids,
		func(_ int, p id.ID[AnnotationUse]) AnnotationUse {
			return id.Wrap(f, p)
		},
	)
}
