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
)

// DeclAnnotationUse is a `@name(args)` annotation use site introduced by
// the protowire v1.2 schema extensions (RFC-001).
//
// Use sites attach to other declarations as leading metadata (on block
// decls like messages, enums, services) or as trailing metadata (on
// single-line decls like type, function, annotation, and eventually
// field and enum-value). The owning declaration's `LeadingAnnotations()`
// accessor exposes the seq.
//
// A use site may also appear as a top-level [DeclAny] when the parser
// recovers from an orphaned annotation (an annotation that did not
// successfully bind to a following declaration). In that case the
// linker is expected to diagnose it.
//
// # Grammar
//
//	DeclAnnotationUse := `@` Path (`(` (Expr `,`?)* `)`)?
//
// Three forms are accepted:
//
//	@Name                          — no args, no parens
//	@Name()                        — empty parens
//	@Name(arg1, name = arg2, ...)  — one or more args
//
// The argument expressions are held as the general [ExprAny] grammar so
// the parser can defer narrow-shape validation to a legalize pass.
type DeclAnnotationUse id.Node[DeclAnnotationUse, *File, *rawDeclAnnotationUse]

type rawDeclAnnotationUse struct {
	args   []withComma[id.Dyn[ExprAny, ExprKind]]
	name   PathID
	at     token.ID
	parens token.ID
}

// DeclAnnotationUseArgs is arguments for [Nodes.NewDeclAnnotationUse].
//
// Set Parens to [token.Zero] for the bare `@Name` form. Arguments are
// added after construction via [DeclAnnotationUse.Args].
type DeclAnnotationUseArgs struct {
	At     token.Token
	Name   Path
	Parens token.Token
}

// AsAny type-erases this declaration value.
//
// See [DeclAny] for more information.
func (d DeclAnnotationUse) AsAny() DeclAny {
	if d.IsZero() {
		return DeclAny{}
	}
	return id.WrapDyn(d.Context(), id.NewDyn(DeclKindAnnotationUse, id.ID[DeclAny](d.ID())))
}

// At returns the leading `@` token for this annotation use site.
//
// May be zero, if the user forgot it.
func (d DeclAnnotationUse) At() token.Token {
	if d.IsZero() {
		return token.Zero
	}

	return id.Wrap(d.Context().Stream(), d.Raw().at)
}

// Name returns the annotation's name path. The path may be a single
// identifier (e.g. `@validate`) or a qualified name (e.g.
// `@myco.validate`).
func (d DeclAnnotationUse) Name() Path {
	if d.IsZero() {
		return Path{}
	}

	return d.Raw().name.In(d.Context())
}

// Parens returns the fused parentheses token wrapping the argument
// list, or zero when the source omitted the argument list entirely
// (the bare `@Name` form).
func (d DeclAnnotationUse) Parens() token.Token {
	if d.IsZero() {
		return token.Zero
	}

	return id.Wrap(d.Context().Stream(), d.Raw().parens)
}

// Args returns the argument list for this annotation use site.
//
// Returns an empty seq for the bare `@Name` and empty-parens `@Name()`
// forms.
func (d DeclAnnotationUse) Args() Commas[ExprAny] {
	type slice = commas[ExprAny, id.Dyn[ExprAny, ExprKind]]
	if d.IsZero() {
		return slice{}
	}
	return slice{
		file: d.Context(),
		SliceInserter: seq.NewSliceInserter(
			&d.Raw().args,
			func(_ int, c withComma[id.Dyn[ExprAny, ExprKind]]) ExprAny {
				return id.WrapDyn(d.Context(), c.Value)
			},
			func(_ int, e ExprAny) withComma[id.Dyn[ExprAny, ExprKind]] {
				d.Context().Nodes().panicIfNotOurs(e.Context())
				return withComma[id.Dyn[ExprAny, ExprKind]]{Value: e.ID()}
			},
		),
	}
}

// Span implements [source.Spanner].
func (d DeclAnnotationUse) Span() source.Span {
	if d.IsZero() {
		return source.Span{}
	}

	return source.Join(d.At(), d.Name(), d.Parens())
}
