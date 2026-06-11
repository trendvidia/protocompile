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
//	DeclAnnotationUse := `@` Path (`(` (Expr `,`?)* `)`)?
//
// The argument expressions are parsed via [parseExpr] and may use the
// full CEL expression grammar. A legalize pass in a subsequent PR
// rejects shapes outside the PR 5 narrow set.
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

// parseAnnotationUseArgs parses a comma-separated list of expression
// arguments from inside an annotation use site's parens.
func parseAnnotationUseArgs(p *parser, c *token.Cursor, decl ast.DeclAnnotationUse, in taxa.Noun) {
	delimited[ast.ExprAny]{
		p:    p,
		c:    c,
		what: taxa.Expr,
		in:   in,

		required: true,
		exhaust:  true,
		parse: func(c *token.Cursor) (ast.ExprAny, bool) {
			e := parseExpr(p, c, in.In())
			return e, !e.IsZero()
		},
		start: canStartExpr,
	}.appendTo(decl.Args())
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
