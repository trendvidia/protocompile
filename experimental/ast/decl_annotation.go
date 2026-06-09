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

// DeclAnnotation is an annotation-declaration introduced by the
// protowire v1.2 schema extensions (RFC-001).
//
// An annotation declaration names a metadata attachment usable at use
// sites via the `@name(args)` syntax.
//
// # Grammar
//
//	DeclAnnotation := `annotation` Ident (`(` (DeclAnnotationParam `,`?)* `)`)? `;`?
//
// The parameter list is optional in the source: both `annotation required;`
// (no parens) and `annotation required();` (empty parens) are accepted
// forms. When parens are absent, [DeclAnnotation.Parens] returns zero.
// Parameters are appended to the returned node via
// [DeclAnnotation.Params].
type DeclAnnotation id.Node[DeclAnnotation, *File, *rawDeclAnnotation]

type rawDeclAnnotation struct {
	params  []withComma[id.ID[DeclAnnotationParam]]
	keyword token.ID
	name    token.ID
	parens  token.ID
	semi    token.ID
}

// DeclAnnotationArgs is arguments for [Nodes.NewDeclAnnotation].
//
// Parameters are added after construction with [DeclAnnotation.Params].
// Set Parens to [token.Zero] for the parameterless form.
type DeclAnnotationArgs struct {
	Keyword   token.Token
	Name      token.Token
	Parens    token.Token
	Semicolon token.Token
}

// AsAny type-erases this declaration value.
//
// See [DeclAny] for more information.
func (d DeclAnnotation) AsAny() DeclAny {
	if d.IsZero() {
		return DeclAny{}
	}
	return id.WrapDyn(d.Context(), id.NewDyn(DeclKindAnnotation, id.ID[DeclAny](d.ID())))
}

// Keyword returns the keyword for this declaration.
func (d DeclAnnotation) Keyword() keyword.Keyword {
	return d.KeywordToken().Keyword()
}

// KeywordToken returns the "annotation" token for this declaration.
func (d DeclAnnotation) KeywordToken() token.Token {
	if d.IsZero() {
		return token.Zero
	}

	return id.Wrap(d.Context().Stream(), d.Raw().keyword)
}

// Name returns the identifier token naming the declared annotation.
//
// May be zero, if the user forgot it.
func (d DeclAnnotation) Name() token.Token {
	if d.IsZero() {
		return token.Zero
	}

	return id.Wrap(d.Context().Stream(), d.Raw().name)
}

// Parens returns the fused parentheses token wrapping the parameter list,
// or zero when the source omitted the parameter list entirely.
func (d DeclAnnotation) Parens() token.Token {
	if d.IsZero() {
		return token.Zero
	}

	return id.Wrap(d.Context().Stream(), d.Raw().parens)
}

// Params returns the parameter list for this declaration.
func (d DeclAnnotation) Params() Commas[DeclAnnotationParam] {
	type slice = commas[DeclAnnotationParam, id.ID[DeclAnnotationParam]]
	if d.IsZero() {
		return slice{}
	}
	return slice{
		file: d.Context(),
		SliceInserter: seq.NewSliceInserter(
			&d.Raw().params,
			func(_ int, c withComma[id.ID[DeclAnnotationParam]]) DeclAnnotationParam {
				return id.Wrap(d.Context(), c.Value)
			},
			func(_ int, p DeclAnnotationParam) withComma[id.ID[DeclAnnotationParam]] {
				d.Context().Nodes().panicIfNotOurs(p.Context())
				return withComma[id.ID[DeclAnnotationParam]]{Value: p.ID()}
			},
		),
	}
}

// Semicolon returns this declaration's ending semicolon.
//
// May be zero, if the user forgot it.
func (d DeclAnnotation) Semicolon() token.Token {
	if d.IsZero() {
		return token.Zero
	}

	return id.Wrap(d.Context().Stream(), d.Raw().semi)
}

// Span implements [source.Spanner].
func (d DeclAnnotation) Span() source.Span {
	if d.IsZero() {
		return source.Span{}
	}

	return source.Join(d.KeywordToken(), d.Name(), d.Parens(), d.Semicolon())
}

// DeclAnnotationParam is one parameter in a [DeclAnnotation] declaration.
//
// The default value is optional:
//
//	rule: expression
//	code: string = ""
//	value: any
//
// When the default value is absent, [DeclAnnotationParam.Equals] and
// [DeclAnnotationParam.Default] both return zero.
//
// # Grammar
//
//	DeclAnnotationParam := Ident `:` Type (`=` Expr)?
type DeclAnnotationParam id.Node[DeclAnnotationParam, *File, *rawDeclAnnotationParam]

type rawDeclAnnotationParam struct {
	ty     id.Dyn[TypeAny, TypeKind]
	deflt  id.Dyn[ExprAny, ExprKind]
	name   token.ID
	colon  token.ID
	equals token.ID
}

// DeclAnnotationParamArgs is arguments for [Nodes.NewDeclAnnotationParam].
//
// Set Equals to [token.Zero] and Default to a zero ExprAny when the
// parameter has no default value.
type DeclAnnotationParamArgs struct {
	Name    token.Token
	Colon   token.Token
	Type    TypeAny
	Equals  token.Token
	Default ExprAny
}

// Name returns the parameter's name token.
//
// May be zero, if the user forgot it.
func (p DeclAnnotationParam) Name() token.Token {
	if p.IsZero() {
		return token.Zero
	}

	return id.Wrap(p.Context().Stream(), p.Raw().name)
}

// Colon returns the colon separating the name from the type.
//
// May be zero, if the user forgot it.
func (p DeclAnnotationParam) Colon() token.Token {
	if p.IsZero() {
		return token.Zero
	}

	return id.Wrap(p.Context().Stream(), p.Raw().colon)
}

// Type returns the parameter's declared type.
//
// May be zero, if the user forgot it.
func (p DeclAnnotationParam) Type() TypeAny {
	if p.IsZero() {
		return TypeAny{}
	}

	return id.WrapDyn(p.Context(), p.Raw().ty)
}

// SetType sets the parameter's declared type.
//
// If passed zero, this clears the type.
func (p DeclAnnotationParam) SetType(ty TypeAny) {
	p.Raw().ty = ty.ID()
}

// Equals returns the equals sign before the default value, or zero when
// the parameter has no default.
func (p DeclAnnotationParam) Equals() token.Token {
	if p.IsZero() {
		return token.Zero
	}

	return id.Wrap(p.Context().Stream(), p.Raw().equals)
}

// Default returns the parameter's default value expression, or zero when
// the parameter has no default.
func (p DeclAnnotationParam) Default() ExprAny {
	if p.IsZero() {
		return ExprAny{}
	}

	return id.WrapDyn(p.Context(), p.Raw().deflt)
}

// SetDefault sets the parameter's default value expression.
//
// If passed zero, this clears the default.
func (p DeclAnnotationParam) SetDefault(expr ExprAny) {
	p.Raw().deflt = expr.ID()
}

// Span implements [source.Spanner].
func (p DeclAnnotationParam) Span() source.Span {
	if p.IsZero() {
		return source.Span{}
	}

	return source.Join(p.Name(), p.Colon(), p.Type(), p.Equals(), p.Default())
}
