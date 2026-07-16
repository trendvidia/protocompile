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
	"github.com/trendvidia/protocompile/internal/taxa"
	"github.com/trendvidia/protocompile/report"
	"github.com/trendvidia/protocompile/seq"
	"github.com/trendvidia/protocompile/source"
	"github.com/trendvidia/protocompile/token"
	"github.com/trendvidia/protocompile/token/keyword"
)

// AnnotationArgBinding pairs one use-site argument with the parameter
// of the resolved target [Annotation] that it binds.
//
// Positional arguments bind parameters left to right; named arguments
// bind the parameter with the matching name. Param is zero when the
// argument does not bind anything — an excess positional argument or
// an unknown name — in which case the B3 validation pass has emitted
// a diagnostic.
type AnnotationArgBinding struct {
	Arg   ast.AnnotationUseArg
	Param AnnotationParam
}

// ArgBindings pairs this use site's arguments with the target
// annotation's parameters, in argument source order.
//
// Returns bindings with a zero Param for every argument when the use
// site's name did not resolve.
func (u AnnotationUse) ArgBindings() []AnnotationArgBinding {
	args := u.AST().Args()
	if args.Len() == 0 {
		return nil
	}

	target := u.Target()
	params := target.Params()

	bindings := make([]AnnotationArgBinding, 0, args.Len())
	var positional int
	for arg := range seq.Values(args) {
		b := AnnotationArgBinding{Arg: arg}
		if arg.IsNamed() {
			name := arg.Name().Text()
			for p := range seq.Values(params) {
				if p.Name() == name {
					b.Param = p
					break
				}
			}
		} else {
			if positional < params.Len() {
				b.Param = params.At(positional)
			}
			positional++
		}
		bindings = append(bindings, b)
	}
	return bindings
}

// scopeName returns the use site's enclosing lexical scope, recorded
// when the use was lowered in B2.
func (u AnnotationUse) scopeName() FullName {
	if u.IsZero() {
		return ""
	}
	return FullName(u.Context().session.intern.Value(u.Raw().scope))
}

// ResolveEnumValueArg resolves a qualified-identifier argument as an
// enum-value reference, relative to this use site's lexical scope.
// Returns a zero [Member] when the name does not resolve to an enum
// value.
//
// This resolution is quiet: no diagnostics are emitted. The B3
// validation pass performs the same resolution with diagnostics; this
// accessor exists so descriptor production can recover the resolved
// value without re-running validation.
func (u AnnotationUse) ResolveEnumValueArg(path ast.Path) Member {
	return u.resolveEnumValue(nil, path)
}

// resolveEnumValue resolves a path as an enum-value reference. Two
// spellings are accepted (RFC-001 §5.1: "bare or qualified value
// name"):
//
//  1. The value's own scoped name — enum values live in their
//     enum's parent scope, so `PUBLIC` or `pkg.PUBLIC`.
//  2. The enum-qualified form `Visibility.PUBLIC` — the prefix
//     resolves to the enum type and the final component names one
//     of its values.
//
// When r is non-nil and neither spelling resolves, the standard
// symbol-resolution diagnostics are emitted for the first form.
func (u AnnotationUse) resolveEnumValue(r *report.Report, path ast.Path) Member {
	if u.IsZero() || path.IsZero() {
		return Member{}
	}

	name := FullName(path.Canonicalized())
	ref := symbolRef{
		File:   u.Context(),
		Report: nil, // Quiet; diagnostics only after both forms fail.
		scope:  u.scopeName(),
		name:   name,
		span:   path,
		accept: func(k SymbolKind) bool { return k == SymbolKindEnumValue },
		want:   taxa.EnumValue,
	}

	if sym := ref.resolve(); sym.Kind() == SymbolKindEnumValue {
		return sym.AsMember()
	}

	// Enum-qualified form: resolve the prefix as an enum type, then
	// find the final component among its values.
	if parent := name.Parent(); parent != "" {
		tyRef := ref
		tyRef.name = parent
		tyRef.accept = func(k SymbolKind) bool { return k == SymbolKindEnum }
		tyRef.want = taxa.EnumType
		if sym := tyRef.resolve(); sym.Kind() == SymbolKindEnum {
			for member := range seq.Values(sym.AsType().Members()) {
				if member.Name() == name.Name() {
					return member
				}
			}
		}
	}

	if r != nil {
		// Re-run the value-name form with diagnostics for the
		// standard "cannot find" error and its suggestions.
		ref.Report = r
		ref.resolve()
	}
	return Member{}
}

// AnnotationFunctionCall is one function-call site extracted from an
// expression-classified annotation argument, per RFC-001 §8.1: a
// maximal qualified identifier immediately followed by `(...)` whose
// name resolves to a visible `function` declaration.
type AnnotationFunctionCall struct {
	// The resolved function declaration.
	Target Function
	// The call site's arity: its count of top-level commas plus one,
	// or zero for empty parentheses.
	Arity int
	// The call site's source span (the identifier through the closing
	// parenthesis), for diagnostics and source-map anchoring.
	Span source.Span
}

// ExtractCalls scans an expression-classified argument's captured
// token range for function-call sites and resolves each against the
// visible `function` declarations, per RFC-001 §8.1.
//
// Extraction is complete with respect to declared functions: every
// maximal `qualifiedIdent (` occurrence whose name resolves (relative
// to the use site's scope) is returned, including calls nested inside
// other calls' argument lists. Names that do not resolve are presumed
// engine builtins — they are not returned and not diagnosed.
//
// Resolution is quiet; arity verification against the declaration is
// the B3 validation pass's job.
func (u AnnotationUse) ExtractCalls(arg ast.AnnotationUseArg) []AnnotationFunctionCall {
	start, end := arg.ValueStart(), arg.ValueEnd()
	if u.IsZero() || start.IsZero() {
		return nil
	}

	var calls []AnnotationFunctionCall
	scanFunctionCalls(u, tokenRange(start, end), &calls)
	return calls
}

// tokenRange returns an iterator over the natural tokens from start
// to end inclusive, at their own nesting depth (fused token trees
// come out as single tokens).
func tokenRange(start, end token.Token) func(yield func(token.Token) bool) {
	return func(yield func(token.Token) bool) {
		c := token.NewCursorAt(start)
		for !c.Done() {
			tok := c.Next()
			if !yield(tok) {
				return
			}
			if tok.ID() == end.ID() {
				return
			}
		}
	}
}

// scanFunctionCalls appends every resolved call site in the token
// sequence to calls, recursing into fused token trees.
func scanFunctionCalls(u AnnotationUse, tokens func(yield func(token.Token) bool), calls *[]AnnotationFunctionCall) {
	// The current maximal qualified-identifier run: ident tokens
	// separated by single dots. Any other token breaks the run.
	var run []token.Token
	var lastDot bool

	flush := func(next token.Token) {
		defer func() { run = run[:0]; lastDot = false }()
		if len(run) == 0 || lastDot || next.Keyword() != keyword.Parens {
			return
		}
		name := joinIdentRun(run)
		sym := symbolRef{
			File:   u.Context(),
			Report: nil, // Quiet: non-resolving names are engine builtins.
			scope:  u.scopeName(),
			name:   FullName(name),
			span:   source.Join(run[0], next),
			accept: func(k SymbolKind) bool { return k == SymbolKindFunction },
			want:   taxa.Noun(keyword.Function),
		}.resolve()
		if sym.Kind() != SymbolKindFunction {
			return
		}
		*calls = append(*calls, AnnotationFunctionCall{
			Target: sym.AsFunction(),
			Arity:  callArity(next),
			Span:   source.Join(run[0], next),
		})
	}

	for tok := range tokens {
		switch {
		case tok.Kind() == token.Ident && (len(run) == 0 || lastDot):
			run = append(run, tok)
			lastDot = false
			continue
		case tok.Keyword() == keyword.Dot && len(run) > 0 && !lastDot:
			lastDot = true
			continue
		}

		flush(tok)

		// Recurse into fused token trees — nested calls inside a
		// call's argument list, brackets, or braces.
		if !tok.IsLeaf() {
			scanFunctionCalls(u, func(yield func(token.Token) bool) {
				c := tok.Children()
				for !c.Done() {
					if !yield(c.Next()) {
						return
					}
				}
			}, calls)
		}
	}
	flush(token.Zero)
}

// joinIdentRun renders a qualified-identifier token run as its dotted
// name.
func joinIdentRun(run []token.Token) string {
	if len(run) == 1 {
		return run[0].Text()
	}
	var n int
	for _, tok := range run {
		n += len(tok.Text()) + 1
	}
	buf := make([]byte, 0, n-1)
	for i, tok := range run {
		if i > 0 {
			buf = append(buf, '.')
		}
		buf = append(buf, tok.Text()...)
	}
	return string(buf)
}

// callArity computes a call site's arity from its fused `(...)`
// token: the count of top-level commas plus one, or zero for empty
// parentheses.
func callArity(parens token.Token) int {
	c := parens.Children()
	if c.Done() {
		return 0
	}
	arity := 1
	for !c.Done() {
		if c.Next().Keyword() == keyword.Comma {
			arity++
		}
	}
	return arity
}
