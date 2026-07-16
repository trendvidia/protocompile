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
	"github.com/trendvidia/protocompile/id"
	"github.com/trendvidia/protocompile/seq"
	"github.com/trendvidia/protocompile/source"
	"github.com/trendvidia/protocompile/token"
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
//	DeclAnnotationUse := `@` Path (`(` (Arg `,`?)* `)`)?
//	Arg               := (Ident `=`)? Capture
//
// Three forms are accepted:
//
//	@Name                          — no args, no parens
//	@Name()                        — empty parens
//	@Name(arg1, name = arg2, ...)  — one or more args
//
// Arguments follow RFC-001 §5.1 capture-then-classify: each argument is
// captured as the raw token range extending to the next `,` or `)` at
// zero delimiter depth (see [AnnotationUseArg]). Arguments that re-parse
// under the value grammar `literal | qualifiedIdent | listLiteral |
// messageLiteral` additionally carry the parsed [ExprAny]; arguments
// that do not (engine-expression fragments) carry only the capture.
type DeclAnnotationUse id.Node[DeclAnnotationUse, *File, *rawDeclAnnotationUse]

type rawDeclAnnotationUse struct {
	args   []withComma[rawAnnotationUseArg]
	name   PathID
	at     token.ID
	parens token.ID
}

type rawAnnotationUseArg struct {
	value    id.Dyn[ExprAny, ExprKind]
	typeName PathID
	name     token.ID
	equals   token.ID
	start    token.ID
	end      token.ID
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
func (d DeclAnnotationUse) Args() Commas[AnnotationUseArg] {
	type slice = commas[AnnotationUseArg, rawAnnotationUseArg]
	if d.IsZero() {
		return slice{}
	}
	return slice{
		file: d.Context(),
		SliceInserter: seq.NewSliceInserter(
			&d.Raw().args,
			func(_ int, c withComma[rawAnnotationUseArg]) AnnotationUseArg {
				return AnnotationUseArg{file: d.Context(), raw: c.Value}
			},
			func(_ int, a AnnotationUseArg) withComma[rawAnnotationUseArg] {
				if a.file != nil && a.file != d.Context() {
					panic("protocompile/ast: attempted to insert an AnnotationUseArg from a different file")
				}
				return withComma[rawAnnotationUseArg]{Value: a.raw}
			},
		),
	}
}

// AnnotationUseArgSpec is the arguments for [DeclAnnotationUse.NewArg].
type AnnotationUseArgSpec struct {
	// Name is the named-argument identifier for the `name = value`
	// form; zero for positional arguments.
	Name token.Token
	// Equals is the `=` token of the `name = value` form; zero for
	// positional arguments.
	Equals token.Token
	// Start and End delimit the captured value: every token from Start
	// to End inclusive, at the argument's nesting depth. Both are
	// always set (the capture must be non-empty).
	Start, End token.Token
	// MessageType is the leading type name of a typed message literal
	// (`Money{...}`); zero otherwise.
	MessageType Path
	// Value is the argument re-parsed under the RFC-001 §5.1 value
	// grammar `literal | qualifiedIdent | listLiteral | messageLiteral`;
	// zero when the capture is an opaque engine-expression fragment.
	Value ExprAny
}

// NewArg assembles an [AnnotationUseArg] for this use site from spec.
// The returned value is appended to the use site via
// [DeclAnnotationUse.Args].
func (d DeclAnnotationUse) NewArg(spec AnnotationUseArgSpec) AnnotationUseArg {
	d.Context().Nodes().panicIfNotOurs(
		spec.Name.Context(), spec.Equals.Context(),
		spec.Start.Context(), spec.End.Context(),
		spec.MessageType.Context(), spec.Value.Context())

	return AnnotationUseArg{
		file: d.Context(),
		raw: rawAnnotationUseArg{
			value:    spec.Value.ID(),
			typeName: spec.MessageType.raw,
			name:     spec.Name.ID(),
			equals:   spec.Equals.ID(),
			start:    spec.Start.ID(),
			end:      spec.End.ID(),
		},
	}
}

// AnnotationUseArg is a single argument of a [DeclAnnotationUse],
// following RFC-001 §5.1 capture-then-classify parsing.
//
// Every argument records its captured token range ([AnnotationUseArg.ValueStart]
// through [AnnotationUseArg.ValueEnd]; [AnnotationUseArg.ValueSpan] for
// the raw text). Arguments that re-parse under the §5.1 value grammar
// `literal | qualifiedIdent | listLiteral | messageLiteral` also carry
// the parsed expression in [AnnotationUseArg.Value]; opaque
// engine-expression fragments leave Value zero. Classification against
// the annotation's parameter types happens at link time.
type AnnotationUseArg struct {
	file *File
	raw  rawAnnotationUseArg
}

// IsZero reports whether this is the zero value.
func (a AnnotationUseArg) IsZero() bool {
	return a.file == nil
}

// Name returns the named-argument identifier for the `name = value`
// form. Zero for positional arguments.
func (a AnnotationUseArg) Name() token.Token {
	if a.IsZero() {
		return token.Zero
	}
	return id.Wrap(a.file.Stream(), a.raw.name)
}

// Equals returns the `=` token of the `name = value` form. Zero for
// positional arguments.
func (a AnnotationUseArg) Equals() token.Token {
	if a.IsZero() {
		return token.Zero
	}
	return id.Wrap(a.file.Stream(), a.raw.equals)
}

// IsNamed reports whether this is a named argument (`name = value`).
func (a AnnotationUseArg) IsNamed() bool {
	return !a.Name().IsZero()
}

// Value returns the argument re-parsed under the RFC-001 §5.1 value
// grammar `literal | qualifiedIdent | listLiteral | messageLiteral`.
//
// Returns a zero [ExprAny] when the capture did not re-parse under
// that grammar — i.e. the argument is an opaque engine-expression
// fragment, legal only when bound to an `expression`-typed parameter
// (diagnosed at link time).
func (a AnnotationUseArg) Value() ExprAny {
	if a.IsZero() {
		return ExprAny{}
	}
	return id.WrapDyn(a.file, a.raw.value)
}

// MessageType returns the leading type name of a typed message-literal
// argument (the `Money` of `Money{currency: "USD"}`), in which case
// [AnnotationUseArg.Value] holds the braced initializer as an
// [ExprDict]. Zero for every other argument shape.
func (a AnnotationUseArg) MessageType() Path {
	if a.IsZero() {
		return Path{}
	}
	return a.raw.typeName.In(a.file)
}

// ValueStart returns the first token of the captured value.
func (a AnnotationUseArg) ValueStart() token.Token {
	if a.IsZero() {
		return token.Zero
	}
	return id.Wrap(a.file.Stream(), a.raw.start)
}

// ValueEnd returns the last token of the captured value (inclusive).
func (a AnnotationUseArg) ValueEnd() token.Token {
	if a.IsZero() {
		return token.Zero
	}
	return id.Wrap(a.file.Stream(), a.raw.end)
}

// ValueSpan returns the span of the captured value — the argument
// without the `name =` prefix. Its Text() is the §5.1 verbatim capture
// (before whitespace trimming).
func (a AnnotationUseArg) ValueSpan() source.Span {
	if a.IsZero() {
		return source.Span{}
	}
	return source.Join(a.ValueStart(), a.ValueEnd())
}

// Span implements [source.Spanner].
func (a AnnotationUseArg) Span() source.Span {
	if a.IsZero() {
		return source.Span{}
	}
	return source.Join(a.Name(), a.Equals(), a.ValueSpan())
}

// Span implements [source.Spanner].
func (d DeclAnnotationUse) Span() source.Span {
	if d.IsZero() {
		return source.Span{}
	}

	return source.Join(d.At(), d.Name(), d.Parens())
}
