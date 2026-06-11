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

// TypeAlias is a `type` declaration introduced by the Protowire
// Schema Extensions (PSE) grammar — see [ast.DeclType].
//
// Type aliases are language sugar that lower to standard protobuf:
// `type Email = string` declares `Email` as a name that fields can
// use in place of `string` (per RFC-001 §5). Annotations attached at
// the alias declaration become part of every use site's annotation
// list, giving authors a single place to refine a primitive base
// type with validation rules and documentation.
//
// Phase scope here is symbol registration plus FDP emission via the
// [pwsv1.FileTypeDecls] file-scope carrier (50403). Field-type
// resolution against type aliases (so that `Email email = 1;`
// produces a `string` field with the alias's annotations expanded
// on the field-level [pwsv1.AnnotationList]) lives in the broader
// field-type resolution stack and is a separate follow-up.
type TypeAlias id.Node[TypeAlias, *File, *rawTypeAlias]

type rawTypeAlias struct {
	def          id.ID[ast.DeclType]
	fqn, name    intern.ID
	baseTypeName intern.ID // canonical text of the base-type expression.

	annotationUses []id.ID[AnnotationUse]
}

// AST returns the declaration for this type alias, if known.
func (t TypeAlias) AST() ast.DeclType {
	if t.IsZero() {
		return ast.DeclType{}
	}
	return id.Wrap(t.Context().AST(), t.Raw().def)
}

// Name returns the alias's declared name, i.e. the last component of
// its full name.
func (t TypeAlias) Name() string {
	return t.FullName().Name()
}

// FullName returns the alias's fully-qualified name.
func (t TypeAlias) FullName() FullName {
	if t.IsZero() {
		return ""
	}
	return FullName(t.Context().session.intern.Value(t.Raw().fqn))
}

// InternedName returns the intern ID for [TypeAlias.FullName]().Name().
func (t TypeAlias) InternedName() intern.ID {
	if t.IsZero() {
		return 0
	}
	return t.Raw().name
}

// InternedFullName returns the intern ID for [TypeAlias.FullName].
func (t TypeAlias) InternedFullName() intern.ID {
	if t.IsZero() {
		return 0
	}
	return t.Raw().fqn
}

// BaseTypeName returns the canonical text of the alias's base-type
// expression as the user wrote it (e.g. "string", or
// "myco.commons.Address"). Field-type resolution against this name
// is deferred to the broader resolution stack — see the package-
// level note on [TypeAlias].
func (t TypeAlias) BaseTypeName() string {
	if t.IsZero() || t.Raw().baseTypeName == 0 {
		return ""
	}
	return t.Context().session.intern.Value(t.Raw().baseTypeName)
}

// Annotations returns the annotation use sites attached to this
// alias (trailing form: `type Email = string @validate(...);`).
// See [Type.Annotations] for the resolution model.
func (t TypeAlias) Annotations() seq.Indexer[AnnotationUse] {
	if t.IsZero() {
		return annotationUses(nil, nil)
	}
	return annotationUses(t.Context(), t.Raw().annotationUses)
}
