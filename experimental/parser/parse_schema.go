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
	"github.com/trendvidia/protocompile/experimental/ast"
	"github.com/trendvidia/protocompile/experimental/internal/errtoken"
	"github.com/trendvidia/protocompile/experimental/internal/taxa"
	"github.com/trendvidia/protocompile/experimental/token"
	"github.com/trendvidia/protocompile/experimental/token/keyword"
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

	semi, err := parseSemi(p, c, in)
	args.Semicolon = semi
	if err != nil && !args.Value.IsZero() {
		p.Error(err)
	}

	return p.NewDeclType(args)
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

	semi, err := parseSemi(p, c, in)
	args.Semicolon = semi
	if err != nil {
		p.Error(err)
	}

	decl := p.NewDeclFunction(args)

	if !args.Parens.IsZero() {
		parseFunctionParams(p, args.Parens.Children(), decl, in)
	}

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

	semi, err := parseSemi(p, c, in)
	args.Semicolon = semi
	if err != nil {
		p.Error(err)
	}

	decl := p.NewDeclAnnotation(args)

	if !args.Parens.IsZero() {
		parseAnnotationParams(p, args.Parens.Children(), decl, in)
	}

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
