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

package parser

import (
	"github.com/trendvidia/protocompile/ast"
	"github.com/trendvidia/protocompile/internal/errtoken"
	"github.com/trendvidia/protocompile/internal/taxa"
	"github.com/trendvidia/protocompile/report"
	"github.com/trendvidia/protocompile/seq"
	"github.com/trendvidia/protocompile/token"
	"github.com/trendvidia/protocompile/token/keyword"
)

// parseTypeDecl parses a protowire v1.2 type-alias declaration:
//
//	type Name = Expr ;
//
// On entry, kw is the "type" keyword and name is the path that was
// already parsed by [parseTypeAndPath] in [parseDecl] as the user-given
// alias name.
func parseTypeDecl(p *parser, c *token.Cursor, kw token.Token, name ast.Path) ast.DeclType {
	in := taxa.Noun(keyword.Type)

	args := ast.DeclTypeArgs{
		Keyword: kw,
		Name:    name.AsIdent(),
	}

	eq, err := parseEquals(p, c, in)
	args.Equals = eq
	if err != nil {
		p.Error(err)
	}

	if !args.Equals.IsZero() || canStartExpr(c.Peek()) {
		args.Value = parseExpr(p, c, in.In())
	}

	trailing := collectTrailingAnnotations(p, c, in)

	semi, err := parseSemi(p, c, in)
	args.Semicolon = semi
	if err != nil && !args.Value.IsZero() {
		p.Error(err)
	}

	decl := p.NewDeclType(args)
	attachAnnotations(decl.Annotations(), trailing)
	return decl
}

// parseFunctionDecl parses a protowire v1.2 function-signature
// declaration:
//
//	function name ( param: type, ... ) [opts] ;
//
// On entry, kw is the "function" keyword and name is the path that was
// already parsed as the function's declared name.
func parseFunctionDecl(p *parser, c *token.Cursor, kw token.Token, name ast.Path) ast.DeclFunction {
	in := taxa.Noun(keyword.Function)

	args := ast.DeclFunctionArgs{
		Keyword: kw,
		Name:    name.AsIdent(),
	}

	if next := c.Peek(); next.Keyword() == keyword.Parens {
		args.Parens = c.Next()
	} else {
		p.Error(errtoken.Unexpected{
			What:  next,
			Where: in.In(),
			Want:  taxa.Noun(keyword.Parens).AsSet(),
		})
	}

	args.Options = tryParseOptions(p, c, in)

	trailing := collectTrailingAnnotations(p, c, in)

	semi, err := parseSemi(p, c, in)
	args.Semicolon = semi
	if err != nil {
		p.Error(err)
	}

	decl := p.NewDeclFunction(args)

	if !args.Parens.IsZero() {
		parseFunctionParams(p, args.Parens.Children(), decl, in)
	}

	attachAnnotations(decl.Annotations(), trailing)
	return decl
}

// parseFunctionParams parses a comma-separated list of function
// parameters (`name: type`) from inside the function declaration's
// parens.
func parseFunctionParams(p *parser, c *token.Cursor, decl ast.DeclFunction, in taxa.Noun) {
	delimited[ast.DeclFunctionParam]{
		p:    p,
		c:    c,
		what: taxa.Ident,
		in:   in,

		required: true,
		exhaust:  true,
		parse: func(c *token.Cursor) (ast.DeclFunctionParam, bool) {
			param := parseFunctionParam(p, c, in)
			return param, !param.IsZero()
		},
		start: canStartPath,
	}.appendTo(decl.Params())
}

func parseFunctionParam(p *parser, c *token.Cursor, in taxa.Noun) ast.DeclFunctionParam {
	args := ast.DeclFunctionParamArgs{}

	next := c.Peek()
	if next.Kind() != token.Ident {
		p.Error(errtoken.Unexpected{
			What:  next,
			Where: in.In(),
			Want:  taxa.Ident.AsSet(),
		})
		return ast.DeclFunctionParam{}
	}
	args.Name = c.Next()

	next = c.Peek()
	if next.Keyword() != keyword.Colon {
		p.Error(errtoken.Unexpected{
			What:  next,
			Where: in.In(),
			Want:  taxa.Noun(keyword.Colon).AsSet(),
		})
	} else {
		args.Colon = c.Next()
	}

	args.Type = parseType(p, c, in.In())

	return p.NewDeclFunctionParam(args)
}

// parseAnnotationDecl parses a protowire v1.2 annotation declaration:
//
//	annotation Name ;
//	annotation Name ( param: type [= default], ... ) ;
//
// The parameter list is optional in the source.
func parseAnnotationDecl(p *parser, c *token.Cursor, kw token.Token, name ast.Path) ast.DeclAnnotation {
	in := taxa.Noun(keyword.Annotation)

	args := ast.DeclAnnotationArgs{
		Keyword: kw,
		Name:    name.AsIdent(),
	}

	if next := c.Peek(); next.Keyword() == keyword.Parens {
		args.Parens = c.Next()
	}

	trailing := collectTrailingAnnotations(p, c, in)

	semi, err := parseSemi(p, c, in)
	args.Semicolon = semi
	if err != nil {
		p.Error(err)
	}

	decl := p.NewDeclAnnotation(args)

	if !args.Parens.IsZero() {
		parseAnnotationParams(p, args.Parens.Children(), decl, in)
	}

	attachAnnotations(decl.Annotations(), trailing)
	return decl
}

// parseAnnotationParams parses a comma-separated list of annotation
// parameters (`name: type [= default]`) from inside the annotation
// declaration's parens.
func parseAnnotationParams(p *parser, c *token.Cursor, decl ast.DeclAnnotation, in taxa.Noun) {
	delimited[ast.DeclAnnotationParam]{
		p:    p,
		c:    c,
		what: taxa.Ident,
		in:   in,

		required: true,
		exhaust:  true,
		parse: func(c *token.Cursor) (ast.DeclAnnotationParam, bool) {
			param := parseAnnotationParam(p, c, in)
			return param, !param.IsZero()
		},
		start: canStartPath,
	}.appendTo(decl.Params())
}

// parseAnnotationUse parses a single `@name(args)` annotation use site.
//
//	DeclAnnotationUse := `@` Path (`(` (Arg `,`?)* `)`)?
//
// Arguments are parsed capture-then-classify per RFC-001 §5.1 — see
// [parseAnnotationUseArgs].
func parseAnnotationUse(p *parser, c *token.Cursor, in taxa.Noun) ast.DeclAnnotationUse {
	at := c.Next()

	args := ast.DeclAnnotationUseArgs{
		At: at,
	}

	if !canStartPath(c.Peek()) {
		p.Error(errtoken.Unexpected{
			What:  c.Peek(),
			Where: in.In(),
			Want:  taxa.Ident.AsSet(),
		})
		return p.NewDeclAnnotationUse(args)
	}

	args.Name = parsePath(p, c)

	if next := c.Peek(); next.Keyword() == keyword.Parens {
		args.Parens = c.Next()
	}

	decl := p.NewDeclAnnotationUse(args)

	if !args.Parens.IsZero() {
		parseAnnotationUseArgs(p, args.Parens.Children(), decl, in)
	}

	return decl
}

// parseAnnotationUseArgs parses a comma-separated list of arguments
// from inside an annotation use site's parens, following RFC-001 §5.1
// capture-then-classify:
//
//  1. An argument that begins `Ident =` — where the token after the
//     `=` is not itself `=` — is a named argument; the name and `=`
//     are peeled off before capture. (`code == "x"` starts with a
//     single `==` token and stays positional.)
//  2. The value is captured as the raw token range extending to the
//     next `,` or `)` at zero delimiter depth. The lexer's fused
//     token trees provide the delimiter balancing — nested `(...)`,
//     `[...]`, `{...}` are single tokens here — and string literals
//     are single tokens, so commas and delimiters inside them never
//     split an argument. An empty capture is a compile error.
//  3. The capture is speculatively re-parsed under the §5.1 value
//     grammar `literal | qualifiedIdent | listLiteral |
//     messageLiteral`. On a match the parsed [ast.ExprAny] is stored
//     alongside the capture; otherwise the capture is opaque — legal
//     only for arguments bound to `expression`-typed parameters,
//     which the link-time classification pass checks.
func parseAnnotationUseArgs(p *parser, c *token.Cursor, decl ast.DeclAnnotationUse, in taxa.Noun) {
	for !c.Done() {
		spec, ok := parseAnnotationUseArg(p, c, in)

		var comma token.Token
		if next := c.Peek(); next.Keyword() == keyword.Comma {
			comma = c.Next()
			if c.Done() {
				p.Errorf("trailing `,` in annotation argument list").Apply(
					report.Snippet(comma),
				)
			}
		}

		if ok {
			decl.Args().AppendComma(decl.NewArg(spec), comma)
		}
	}
}

// parseAnnotationUseArg parses a single annotation argument up to (but
// not including) the next top-level comma. Reports false when the
// argument is empty (in which case a diagnostic was already emitted).
func parseAnnotationUseArg(p *parser, c *token.Cursor, in taxa.Noun) (ast.AnnotationUseArgSpec, bool) {
	var spec ast.AnnotationUseArgSpec

	// Named-argument peel: `Ident =` where the token after `=` is not
	// itself `=`. A `==` lexes as one token, so expression fragments
	// like `code == "x"` never match.
	if name := c.Peek(); name.Kind() == token.Ident {
		clone := c.Clone()
		clone.Next()
		if eq := clone.Peek(); eq.Keyword() == keyword.Assign {
			clone.Next()
			if clone.Peek().Keyword() != keyword.Assign {
				spec.Name = c.Next()
				spec.Equals = c.Next()
			}
		}
	}

	// Capture: everything to the next top-level `,` (or the end of the
	// parens). Fused bracket tokens keep nested commas out of this
	// stream, and string literals are single opaque tokens.
	var tokens []token.Token
	for !c.Done() && c.Peek().Keyword() != keyword.Comma {
		tokens = append(tokens, c.Next())
	}

	if len(tokens) == 0 {
		what := taxa.Expr.AsSet()
		err := errtoken.Unexpected{
			What:  c.Peek(),
			Where: in.In(),
			Want:  what,
		}
		if c.Done() {
			_, span := c.SeekToEnd()
			err.What = span
			err.Got = taxa.EOF
		}
		p.Error(err)
		return spec, false
	}

	spec.Start = tokens[0]
	spec.End = tokens[len(tokens)-1]
	spec.Value, spec.MessageType = classifyAnnotationArgValue(p, tokens)
	return spec, true
}

// classifyAnnotationArgValue speculatively re-parses a captured
// argument under the RFC-001 §5.1 value grammar:
//
//	annotArgValue  ::= literal | qualifiedIdent
//	literal        ::= scalarLit | listLiteral | messageLiteral
//	scalarLit      ::= strLit | intLit | floatLit ("-" forms included)
//	listLiteral    ::= "[" (literalValue ("," literalValue)*)? "]"
//	messageLiteral ::= qualifiedIdent? "{" (fieldInit ("," fieldInit)*)? "}"
//	fieldInit      ::= Ident ":" literalValue
//
// Returns a zero [ast.ExprAny] when the capture does not match — the
// argument is then an opaque engine-expression fragment. The second
// return is the leading type name of a typed message literal.
//
// The token shapes are fully pre-checked before any node construction,
// so a failed classification emits no diagnostics and allocates no
// nodes.
func classifyAnnotationArgValue(p *parser, tokens []token.Token) (ast.ExprAny, ast.Path) {
	switch {
	case len(tokens) == 1 && (tokens[0].Kind() == token.String || tokens[0].Kind() == token.Number):
		return ast.ExprLiteral{File: p.File(), Token: tokens[0]}.AsAny(), ast.Path{}

	case len(tokens) == 2 && tokens[0].Keyword() == keyword.Sub && tokens[1].Kind() == token.Number:
		return p.NewExprPrefixed(ast.ExprPrefixedArgs{
			Prefix: tokens[0],
			Expr:   ast.ExprLiteral{File: p.File(), Token: tokens[1]}.AsAny(),
		}).AsAny(), ast.Path{}

	case isQualifiedIdent(tokens):
		return ast.ExprPath{Path: pathOf(p, tokens[0])}.AsAny(), ast.Path{}

	case len(tokens) == 1 && tokens[0].Keyword() == keyword.Brackets:
		if arr, ok := classifyListLiteral(p, tokens[0]); ok {
			return arr, ast.Path{}
		}

	case tokens[len(tokens)-1].Keyword() == keyword.Braces &&
		(len(tokens) == 1 || isQualifiedIdent(tokens[:len(tokens)-1])):
		if dict, ok := classifyMessageLiteral(p, tokens[len(tokens)-1]); ok {
			var typeName ast.Path
			if len(tokens) > 1 {
				typeName = pathOf(p, tokens[0])
			}
			return dict, typeName
		}
	}

	return ast.ExprAny{}, ast.Path{}
}

// classifyListLiteral re-parses a fused `[...]` token as a §5.1 list
// literal whose elements are each `literalValue ::= literal |
// qualifiedIdent`. Reports false when any element fails to classify
// (making the whole capture opaque) or the list has empty elements.
func classifyListLiteral(p *parser, brackets token.Token) (ast.ExprAny, bool) {
	elements, commas, ok := splitTopLevelCommas(brackets.Children())
	if !ok {
		return ast.ExprAny{}, false
	}

	exprs := make([]ast.ExprAny, len(elements))
	for i, elem := range elements {
		expr, typeName := classifyAnnotationArgValue(p, elem)
		// Typed message literals are legal list elements, but the
		// element expr loses the type path here: element-level typed
		// messages are rare and the linker re-derives the type from
		// the list's context. Reject for now so the S2 element-typing
		// story stays explicit — the argument stays opaque and the
		// link-time classifier diagnoses it if the param isn't
		// expression-typed.
		if expr.IsZero() || !typeName.IsZero() {
			return ast.ExprAny{}, false
		}
		exprs[i] = expr
	}

	array := p.NewExprArray(brackets)
	for i, expr := range exprs {
		array.Elements().AppendComma(expr, commas[i])
	}
	return array.AsAny(), true
}

// classifyMessageLiteral re-parses a fused `{...}` token as a §5.1
// message literal: comma-separated `Ident : literalValue` field
// initializers. Reports false when any field fails that shape.
func classifyMessageLiteral(p *parser, braces token.Token) (ast.ExprAny, bool) {
	fields, commas, ok := splitTopLevelCommas(braces.Children())
	if !ok {
		return ast.ExprAny{}, false
	}

	type fieldParts struct {
		name  token.Token
		colon token.Token
		value ast.ExprAny
	}
	parts := make([]fieldParts, len(fields))
	for i, field := range fields {
		if len(field) < 3 || field[0].Kind() != token.Ident || field[1].Keyword() != keyword.Colon {
			return ast.ExprAny{}, false
		}
		value, typeName := classifyAnnotationArgValue(p, field[2:])
		if value.IsZero() || !typeName.IsZero() {
			return ast.ExprAny{}, false
		}
		parts[i] = fieldParts{name: field[0], colon: field[1], value: value}
	}

	dict := p.NewExprDict(braces)
	for i, f := range parts {
		field := p.NewExprField(ast.ExprFieldArgs{
			Key:   ast.ExprPath{Path: pathOf(p, f.name)}.AsAny(),
			Colon: f.colon,
			Value: f.value,
		})
		dict.Elements().AppendComma(field, commas[i])
	}
	return dict.AsAny(), true
}

// splitTopLevelCommas splits the children of a fused bracket token
// into comma-separated segments, returning the segments and the comma
// token following each (zero for the last). Reports false when a
// segment is empty (leading, trailing, or doubled commas) — except
// that zero segments (an empty list `[]` or `{}`) is legal.
func splitTopLevelCommas(c *token.Cursor) (segments [][]token.Token, commas []token.Token, ok bool) {
	var current []token.Token
	for !c.Done() {
		next := c.Next()
		if next.Keyword() != keyword.Comma {
			current = append(current, next)
			continue
		}
		if len(current) == 0 {
			return nil, nil, false
		}
		segments = append(segments, current)
		commas = append(commas, next)
		current = nil
	}
	switch {
	case len(current) > 0:
		segments = append(segments, current)
		commas = append(commas, token.Zero)
	case len(segments) > 0:
		// Trailing comma.
		return nil, nil, false
	}
	return segments, commas, true
}

// isQualifiedIdent reports whether tokens form a qualified identifier:
// `Ident ("." Ident)*`, with an optional leading `.` for absolute
// references.
func isQualifiedIdent(tokens []token.Token) bool {
	if len(tokens) == 0 {
		return false
	}
	wantIdent := true
	for i, tok := range tokens {
		if i == 0 && tok.Keyword() == keyword.Dot {
			continue // Absolute path.
		}
		if wantIdent {
			if tok.Kind() != token.Ident {
				return false
			}
		} else if tok.Keyword() != keyword.Dot {
			return false
		}
		wantIdent = !wantIdent
	}
	return !wantIdent // Must end on an identifier.
}

// pathOf re-parses the path starting at tok, which the caller has
// already shape-checked with [isQualifiedIdent]. [parsePath] consumes
// exactly the qualified-identifier tokens: it stops at the first
// token that is neither `.` nor an identifier.
func pathOf(p *parser, tok token.Token) ast.Path {
	return parsePath(p, token.NewCursorAt(tok))
}

// collectLeadingAnnotations consumes a run of `@name(args)` use sites
// from the cursor. Used by parseDecl to gather annotations that will
// attach to the next declaration.
func collectLeadingAnnotations(p *parser, c *token.Cursor, in taxa.Noun) []ast.DeclAnnotationUse {
	var anns []ast.DeclAnnotationUse
	for c.Peek().Keyword() == keyword.At {
		ann := parseAnnotationUse(p, c, in)
		if ann.IsZero() {
			break
		}
		anns = append(anns, ann)
	}
	return anns
}

// collectTrailingAnnotations is the trailing-placement variant of
// [collectLeadingAnnotations]. Called by parseTypeDecl, parseFunctionDecl,
// and parseAnnotationDecl right before they consume the semicolon.
func collectTrailingAnnotations(p *parser, c *token.Cursor, in taxa.Noun) []ast.DeclAnnotationUse {
	return collectLeadingAnnotations(p, c, in)
}

// attachAnnotations appends each annotation in anns onto the target
// seq. The annotations preserve source order.
func attachAnnotations(target seq.Inserter[ast.DeclAnnotationUse], anns []ast.DeclAnnotationUse) {
	for _, ann := range anns {
		seq.Append(target, ann)
	}
}

// attachLeadingAnnotations inserts leading annotations at the front of
// the target seq, preserving their source order ahead of any trailing
// annotations the inner decl parser already appended.
func attachLeadingAnnotations(target seq.Inserter[ast.DeclAnnotationUse], anns []ast.DeclAnnotationUse) {
	for i, ann := range anns {
		target.Insert(i, ann)
	}
}

// parseAnnotatedDecl handles the `@name(args) ... decl` form. On
// entry the cursor is positioned at `@`. It collects every consecutive
// `@name(args)` use site, then recursively parses the following
// declaration and attaches them as leading metadata.
//
// If the run of annotations is not followed by a declaration (orphan
// case), the first annotation is returned as a top-level [ast.DeclAny]
// so the linker can diagnose it. The remaining orphan annotations are
// dropped in this PR; richer recovery can land in a follow-up.
func parseAnnotatedDecl(p *parser, c *token.Cursor, in taxa.Noun) ast.DeclAny {
	anns := collectLeadingAnnotations(p, c, in)
	if len(anns) == 0 {
		return ast.DeclAny{}
	}

	next := c.Peek()
	if next.IsZero() || next.Keyword() == keyword.Semi {
		// No following decl: the annotation(s) are orphans. Emit the
		// first as a top-level decl so the file has something to
		// diagnose against. (Subsequent orphans are silently dropped
		// for now.)
		return anns[0].AsAny()
	}

	inner := parseDecl(p, c, in)
	if inner.IsZero() {
		return anns[0].AsAny()
	}

	target, ok := annotationsOf(inner)
	if !ok {
		// The inner decl kind does not carry annotations (e.g. syntax,
		// package, import). The legalize layer will diagnose; for now
		// just drop the orphans by returning the inner decl as-is.
		return inner
	}
	attachLeadingAnnotations(target, anns)
	return inner
}

// annotationsOf returns the Annotations seq for a decl that supports
// annotation attachment, plus true. For decls that do not support
// annotations it returns the zero seq and false.
func annotationsOf(decl ast.DeclAny) (seq.Inserter[ast.DeclAnnotationUse], bool) {
	switch decl.Kind() {
	case ast.DeclKindDef:
		return decl.AsDef().Annotations(), true
	case ast.DeclKindType:
		return decl.AsType().Annotations(), true
	case ast.DeclKindFunction:
		return decl.AsFunction().Annotations(), true
	case ast.DeclKindAnnotation:
		return decl.AsAnnotation().Annotations(), true
	default:
		return nil, false
	}
}

func parseAnnotationParam(p *parser, c *token.Cursor, in taxa.Noun) ast.DeclAnnotationParam {
	args := ast.DeclAnnotationParamArgs{}

	next := c.Peek()
	if next.Kind() != token.Ident {
		p.Error(errtoken.Unexpected{
			What:  next,
			Where: in.In(),
			Want:  taxa.Ident.AsSet(),
		})
		return ast.DeclAnnotationParam{}
	}
	args.Name = c.Next()

	next = c.Peek()
	if next.Keyword() != keyword.Colon {
		p.Error(errtoken.Unexpected{
			What:  next,
			Where: in.In(),
			Want:  taxa.Noun(keyword.Colon).AsSet(),
		})
	} else {
		args.Colon = c.Next()
	}

	args.Type = parseType(p, c, in.In())

	if next := c.Peek(); next.Keyword() == keyword.Assign {
		args.Equals = c.Next()
		args.Default = parseExpr(p, c, in.In())
	}

	return p.NewDeclAnnotationParam(args)
}
