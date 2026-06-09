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

package ast

import (
	"github.com/trendvidia/protocompile/experimental/id"
	"github.com/trendvidia/protocompile/experimental/seq"
	"github.com/trendvidia/protocompile/experimental/source"
	"github.com/trendvidia/protocompile/experimental/token"
	"github.com/trendvidia/protocompile/experimental/token/keyword"
)

// DeclType is a type-alias declaration introduced by the protowire v1.2
// schema extensions (RFC-001).
//
// # Grammar
//
//	DeclType := `type` Ident `=` Expr `;`?
//
// The value expression is the base type the alias refines. In well-formed
// input it is a path expression naming a Protobuf scalar or another
// declared type; the parser accepts the broader ExprAny grammar so the
// linker can issue precise diagnostics on malformed input.
type DeclType id.Node[DeclType, *File, *rawDeclType]

type rawDeclType struct {
	annotations []id.ID[DeclAnnotationUse]
	value       id.Dyn[ExprAny, ExprKind]
	keyword     token.ID
	name        token.ID
	equals      token.ID
	semi        token.ID
}

// DeclTypeArgs is arguments for [Nodes.NewDeclType].
type DeclTypeArgs struct {
	Keyword   token.Token
	Name      token.Token
	Equals    token.Token
	Value     ExprAny
	Semicolon token.Token
}

// AsAny type-erases this declaration value.
//
// See [DeclAny] for more information.
func (d DeclType) AsAny() DeclAny {
	if d.IsZero() {
		return DeclAny{}
	}
	return id.WrapDyn(d.Context(), id.NewDyn(DeclKindType, id.ID[DeclAny](d.ID())))
}

// Keyword returns the keyword for this declaration.
func (d DeclType) Keyword() keyword.Keyword {
	return d.KeywordToken().Keyword()
}

// KeywordToken returns the "type" token for this declaration.
func (d DeclType) KeywordToken() token.Token {
	if d.IsZero() {
		return token.Zero
	}

	return id.Wrap(d.Context().Stream(), d.Raw().keyword)
}

// Name returns the identifier token naming the declared type.
//
// May be zero, if the user forgot it.
func (d DeclType) Name() token.Token {
	if d.IsZero() {
		return token.Zero
	}

	return id.Wrap(d.Context().Stream(), d.Raw().name)
}

// Equals returns the equals sign between the name and the value.
//
// May be zero, if the user forgot it.
func (d DeclType) Equals() token.Token {
	if d.IsZero() {
		return token.Zero
	}

	return id.Wrap(d.Context().Stream(), d.Raw().equals)
}

// Value returns the base-type expression for this declaration.
//
// May be zero, if the user wrote something like type X = ;.
func (d DeclType) Value() ExprAny {
	if d.IsZero() {
		return ExprAny{}
	}

	return id.WrapDyn(d.Context(), d.Raw().value)
}

// SetValue sets the expression for this declaration's value.
//
// If passed zero, this clears the value.
func (d DeclType) SetValue(expr ExprAny) {
	d.Raw().value = expr.ID()
}

// Annotations returns this declaration's annotation use sites.
//
// For [DeclType], annotations attach as trailing metadata (between the
// value and the semicolon). The seq is empty unless the parser has
// successfully attached at least one annotation.
func (d DeclType) Annotations() seq.Inserter[DeclAnnotationUse] {
	if d.IsZero() {
		return seq.EmptySliceInserter[DeclAnnotationUse, id.ID[DeclAnnotationUse]]()
	}

	return seq.NewSliceInserter(&d.Raw().annotations,
		func(_ int, e id.ID[DeclAnnotationUse]) DeclAnnotationUse {
			return id.Wrap(d.Context(), e)
		},
		func(_ int, a DeclAnnotationUse) id.ID[DeclAnnotationUse] {
			d.Context().Nodes().panicIfNotOurs(a.Context())
			return a.ID()
		},
	)
}

// Semicolon returns this declaration's ending semicolon.
//
// May be zero, if the user forgot it.
func (d DeclType) Semicolon() token.Token {
	if d.IsZero() {
		return token.Zero
	}

	return id.Wrap(d.Context().Stream(), d.Raw().semi)
}

// Span implements [source.Spanner].
func (d DeclType) Span() source.Span {
	if d.IsZero() {
		return source.Span{}
	}

	return source.Join(d.KeywordToken(), d.Name(), d.Equals(), d.Value(), d.Semicolon())
}
