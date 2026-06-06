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

// DeclFunction is a function-signature declaration introduced by the
// protowire v1.2 schema extensions (RFC-001).
//
// A function declaration names a signature for a validation predicate.
// The body is implemented by the engine runtime; this node only carries
// the signature.
//
// # Grammar
//
//	DeclFunction := `function` Ident `(` (DeclFunctionParam `,`?)* `)` CompactOptions? `;`?
//
// Parameters are appended to the returned node via [DeclFunction.Params].
type DeclFunction id.Node[DeclFunction, *File, *rawDeclFunction]

type rawDeclFunction struct {
	keyword token.ID
	name    token.ID
	parens  token.ID
	params  []withComma[id.ID[DeclFunctionParam]]
	options id.ID[CompactOptions]
	semi    token.ID
}

// DeclFunctionArgs is arguments for [Nodes.NewDeclFunction].
//
// Parameters are added after construction with [DeclFunction.Params].
type DeclFunctionArgs struct {
	Keyword   token.Token
	Name      token.Token
	Parens    token.Token
	Options   CompactOptions
	Semicolon token.Token
}

// AsAny type-erases this declaration value.
//
// See [DeclAny] for more information.
func (d DeclFunction) AsAny() DeclAny {
	if d.IsZero() {
		return DeclAny{}
	}
	return id.WrapDyn(d.Context(), id.NewDyn(DeclKindFunction, id.ID[DeclAny](d.ID())))
}

// Keyword returns the keyword for this declaration.
func (d DeclFunction) Keyword() keyword.Keyword {
	return d.KeywordToken().Keyword()
}

// KeywordToken returns the "function" token for this declaration.
func (d DeclFunction) KeywordToken() token.Token {
	if d.IsZero() {
		return token.Zero
	}

	return id.Wrap(d.Context().Stream(), d.Raw().keyword)
}

// Name returns the identifier token naming the declared function.
//
// May be zero, if the user forgot it.
func (d DeclFunction) Name() token.Token {
	if d.IsZero() {
		return token.Zero
	}

	return id.Wrap(d.Context().Stream(), d.Raw().name)
}

// Parens returns the fused parentheses token wrapping this declaration's
// parameter list.
//
// May be zero, if the user forgot it.
func (d DeclFunction) Parens() token.Token {
	if d.IsZero() {
		return token.Zero
	}

	return id.Wrap(d.Context().Stream(), d.Raw().parens)
}

// Params returns the parameter list for this declaration.
func (d DeclFunction) Params() Commas[DeclFunctionParam] {
	type slice = commas[DeclFunctionParam, id.ID[DeclFunctionParam]]
	if d.IsZero() {
		return slice{}
	}
	return slice{
		file: d.Context(),
		SliceInserter: seq.NewSliceInserter(
			&d.Raw().params,
			func(_ int, c withComma[id.ID[DeclFunctionParam]]) DeclFunctionParam {
				return id.Wrap(d.Context(), c.Value)
			},
			func(_ int, p DeclFunctionParam) withComma[id.ID[DeclFunctionParam]] {
				d.Context().Nodes().panicIfNotOurs(p.Context())
				return withComma[id.ID[DeclFunctionParam]]{Value: p.ID()}
			},
		),
	}
}

// Options returns the compact options list for this declaration.
func (d DeclFunction) Options() CompactOptions {
	if d.IsZero() {
		return CompactOptions{}
	}

	return id.Wrap(d.Context(), d.Raw().options)
}

// SetOptions sets the compact options list for this declaration.
//
// Setting it to a zero Options clears it.
func (d DeclFunction) SetOptions(opts CompactOptions) {
	d.Raw().options = opts.ID()
}

// Semicolon returns this declaration's ending semicolon.
//
// May be zero, if the user forgot it.
func (d DeclFunction) Semicolon() token.Token {
	if d.IsZero() {
		return token.Zero
	}

	return id.Wrap(d.Context().Stream(), d.Raw().semi)
}

// Span implements [source.Spanner].
func (d DeclFunction) Span() source.Span {
	if d.IsZero() {
		return source.Span{}
	}

	return source.Join(
		d.KeywordToken(), d.Name(), d.Parens(),
		d.Options(), d.Semicolon(),
	)
}

// DeclFunctionParam is one parameter in a [DeclFunction] declaration:
//
//	value: string
//	pattern: string
//	msg: myco.commons.Address
//
// # Grammar
//
//	DeclFunctionParam := Ident `:` Type
type DeclFunctionParam id.Node[DeclFunctionParam, *File, *rawDeclFunctionParam]

type rawDeclFunctionParam struct {
	ty    id.Dyn[TypeAny, TypeKind]
	name  token.ID
	colon token.ID
}

// DeclFunctionParamArgs is arguments for [Nodes.NewDeclFunctionParam].
type DeclFunctionParamArgs struct {
	Name  token.Token
	Colon token.Token
	Type  TypeAny
}

// Name returns the parameter's name token.
//
// May be zero, if the user forgot it.
func (p DeclFunctionParam) Name() token.Token {
	if p.IsZero() {
		return token.Zero
	}

	return id.Wrap(p.Context().Stream(), p.Raw().name)
}

// Colon returns the colon separating the name from the type.
//
// May be zero, if the user forgot it.
func (p DeclFunctionParam) Colon() token.Token {
	if p.IsZero() {
		return token.Zero
	}

	return id.Wrap(p.Context().Stream(), p.Raw().colon)
}

// Type returns the parameter's declared type.
//
// May be zero, if the user forgot it.
func (p DeclFunctionParam) Type() TypeAny {
	if p.IsZero() {
		return TypeAny{}
	}

	return id.WrapDyn(p.Context(), p.Raw().ty)
}

// SetType sets the parameter's declared type.
//
// If passed zero, this clears the type.
func (p DeclFunctionParam) SetType(ty TypeAny) {
	p.Raw().ty = ty.ID()
}

// Span implements [source.Spanner].
func (p DeclFunctionParam) Span() source.Span {
	if p.IsZero() {
		return source.Span{}
	}

	return source.Join(p.Name(), p.Colon(), p.Type())
}
