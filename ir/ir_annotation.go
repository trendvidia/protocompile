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

type rawAnnotation struct {
	def       id.ID[ast.DeclAnnotation]
	fqn, name intern.ID
	params    []id.ID[AnnotationParam]
}

type rawAnnotationParam struct {
	def    id.ID[ast.DeclAnnotationParam]
	parent id.ID[Annotation]
	name   intern.ID
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
