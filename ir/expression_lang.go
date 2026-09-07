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
	"fmt"
	"strings"

	"github.com/trendvidia/protocompile/ast"
	"github.com/trendvidia/protocompile/report"
	"github.com/trendvidia/protocompile/source"
)

// This file checks expression-classified annotation arguments against
// the RFC-001 §5.4 expression language — the one language every
// `@validate` rule is written in (protowire#282). The compiler's
// obligations there: reject a capture that is not an expression, a call
// to a name that is neither a visible `function` declaration nor a §5.4
// builtin, and a builtin called with the wrong arity. Declared-function
// arity is [verifyCallArities]'s job; operand kinds are checked by the
// engine at evaluation, not here.
//
// The grammar is documented on [parseExpression].

// exprBuiltin describes one §5.4 builtin: how many arguments it takes
// beyond its receiver, and whether it has a receiver at all.
type exprBuiltin struct {
	args     int
	receiver bool
}

// exprBuiltins is the complete §5.4 builtin set. A receiver builtin is
// callable in method form (`this.size()`) and in function form with the
// receiver first (`size(this)`); `now()` has no receiver.
var exprBuiltins = map[string]exprBuiltin{
	"size":        {args: 0, receiver: true},
	"starts_with": {args: 1, receiver: true},
	"ends_with":   {args: 1, receiver: true},
	"contains":    {args: 1, receiver: true},
	"matches":     {args: 1, receiver: true},
	"now":         {args: 0, receiver: false},
}

// exprBuiltinList renders the builtin set for diagnostics, in a fixed
// order.
const exprBuiltinList = "size, starts_with, ends_with, contains, matches, now"

// validateExpressionLanguage parses an expression-classified argument's
// capture under the §5.4 grammar and diagnoses what the compiler owns:
// syntax, name resolution against declared functions and builtins, and
// builtin arity. Declared-function call sites are the ones
// [AnnotationUse.ExtractCalls] resolved; every other call must be a
// builtin.
func validateExpressionLanguage(r *report.Report, u AnnotationUse, target Annotation, param AnnotationParam, arg ast.AnnotationUseArg) {
	span := arg.ValueSpan()
	if span.IsZero() {
		return
	}
	text := span.Text()
	sub := func(start, end int) source.Span {
		return span.File.Span(span.Start+start, span.Start+end)
	}

	calls, perr := parseExpression(text)
	if perr != nil {
		r.Errorf("argument %q for `%s` is not an expression: %s",
			param.Name(), target.FullName(), perr.msg,
		).Apply(
			report.Snippet(sub(perr.start, perr.end)),
			report.Notef("RFC-001 §5.4: comparisons, `in`, `&&` `||` `!`, literals, `this`, "+
				"builtin methods on `this`, and declared-function calls"),
		)
		return
	}

	declared := make(map[int]bool)
	for _, call := range u.ExtractCalls(arg) {
		declared[call.Span.Start] = true
	}
	for _, call := range calls {
		if !call.method && declared[span.Start+call.start] {
			continue // A declared function: arity is verifyCallArities's.
		}
		builtin, ok := exprBuiltins[call.name]
		switch {
		case !ok && call.method:
			r.Errorf("unknown method `%s` on `this` in argument %q for `%s`",
				call.name, param.Name(), target.FullName(),
			).Apply(
				report.Snippet(sub(call.nameStart, call.nameEnd)),
				report.Helpf("only builtins are called on `this` (%s); a declared function is called as `%s(this, …)`",
					exprBuiltinList, call.name),
			)
		case !ok:
			r.Errorf("unknown function `%s` in argument %q for `%s`: neither a visible `function` declaration nor a builtin",
				call.name, param.Name(), target.FullName(),
			).Apply(
				report.Snippet(sub(call.nameStart, call.nameEnd)),
				report.Helpf("builtins: %s", exprBuiltinList),
			)
		default:
			checkBuiltinArity(r, call, builtin, sub)
		}
	}
}

// checkBuiltinArity diagnoses a builtin called with the wrong number of
// arguments, or in the form it does not have.
func checkBuiltinArity(r *report.Report, call exprCall, builtin exprBuiltin, sub func(int, int) source.Span) {
	at := report.Snippet(sub(call.start, call.end))
	switch {
	case call.method && !builtin.receiver:
		r.Errorf("`%s()` has no receiver: write `%s()`, not `this.%s()`", call.name, call.name, call.name).Apply(at)
	case call.method && call.args != builtin.args:
		r.Errorf("`this.%s(…)` takes %d argument(s), got %d", call.name, builtin.args, call.args).Apply(at)
	case !call.method && builtin.receiver && call.args != builtin.args+1:
		r.Errorf("`%s(…)` takes %d argument(s) in function form, the receiver first, got %d",
			call.name, builtin.args+1, call.args).Apply(at)
	case !call.method && !builtin.receiver && call.args != builtin.args:
		r.Errorf("`%s()` takes no arguments, got %d", call.name, call.args).Apply(at)
	}
}

// exprTokKind classifies a token of the expression language.
type exprTokKind uint8

const (
	exprTokEOF exprTokKind = iota
	exprTokIdent
	exprTokNumber
	exprTokString
	exprTokPunct // One of ( ) [ ] , . ! < > && || == != <= >=.
)

// exprToken is one token of a capture, with its byte range.
type exprToken struct {
	text       string
	start, end int
	kind       exprTokKind
}

// exprError is a syntax error at a byte range of the capture.
type exprError struct {
	msg        string
	start, end int
}

// exprCall is one call site the parser found, with byte ranges into the
// capture.
type exprCall struct {
	name               string // As written: `matches`, `t.matches`, or the method name.
	nameStart, nameEnd int
	start, end         int // The whole call, through its closing parenthesis.
	args               int
	method             bool // `this.m(…)` rather than `m(…)`.
}

type exprLexer struct {
	src string
	pos int
}

func isExprDigit(c byte) bool { return c >= '0' && c <= '9' }

func isExprIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isExprIdentByte(c byte) bool { return isExprIdentStart(c) || isExprDigit(c) }

// skipSpace advances past whitespace and comments; a capture can span
// lines and carry comments like any other proto source.
func (l *exprLexer) skipSpace() {
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			l.pos++
		case c == '/' && strings.HasPrefix(l.src[l.pos:], "//"):
			for l.pos < len(l.src) && l.src[l.pos] != '\n' {
				l.pos++
			}
		case c == '/' && strings.HasPrefix(l.src[l.pos:], "/*"):
			end := strings.Index(l.src[l.pos+2:], "*/")
			if end < 0 {
				l.pos = len(l.src)
			} else {
				l.pos += 2 + end + 2
			}
		default:
			return
		}
	}
}

func (l *exprLexer) next() (exprToken, *exprError) {
	l.skipSpace()
	start := l.pos
	if l.pos >= len(l.src) {
		return exprToken{kind: exprTokEOF, start: start, end: start}, nil
	}
	c := l.src[l.pos]
	switch {
	case c == '"' || c == '\'':
		return l.lexString(start, c)
	case isExprDigit(c) || (c == '-' && l.pos+1 < len(l.src) && isExprDigit(l.src[l.pos+1])):
		return l.lexNumber(start), nil
	case isExprIdentStart(c):
		for l.pos < len(l.src) && isExprIdentByte(l.src[l.pos]) {
			l.pos++
		}
		return exprToken{kind: exprTokIdent, start: start, end: l.pos, text: l.src[start:l.pos]}, nil
	}
	for _, op := range []string{"&&", "||", "==", "!=", "<=", ">="} {
		if strings.HasPrefix(l.src[l.pos:], op) {
			l.pos += len(op)
			return exprToken{kind: exprTokPunct, start: start, end: l.pos, text: op}, nil
		}
	}
	if strings.IndexByte("()[],.!<>", c) >= 0 {
		l.pos++
		return exprToken{kind: exprTokPunct, start: start, end: l.pos, text: string(c)}, nil
	}
	msg := fmt.Sprintf("unexpected character %q", c)
	switch c {
	case '+', '-', '*', '/', '%':
		msg += ": the expression language has no arithmetic"
	case '=':
		msg += ": equality is `==`"
	case '&', '|':
		msg += ": the boolean operators are `&&` and `||`"
	case '?', ':':
		msg += ": the expression language has no conditional operator"
	}
	return exprToken{}, &exprError{msg: msg, start: start, end: start + 1}
}

func (l *exprLexer) lexString(start int, quote byte) (exprToken, *exprError) {
	l.pos++
	for l.pos < len(l.src) && l.src[l.pos] != quote {
		if l.src[l.pos] == '\\' {
			l.pos++
		}
		l.pos++
	}
	if l.pos >= len(l.src) {
		return exprToken{}, &exprError{msg: "unterminated string literal", start: start, end: len(l.src)}
	}
	l.pos++
	return exprToken{kind: exprTokString, start: start, end: l.pos, text: l.src[start:l.pos]}, nil
}

func (l *exprLexer) lexNumber(start int) exprToken {
	if l.src[l.pos] == '-' {
		l.pos++
	}
	seenDot := false
	for l.pos < len(l.src) {
		ch := l.src[l.pos]
		if isExprDigit(ch) {
			l.pos++
			continue
		}
		if ch == '.' && !seenDot && l.pos+1 < len(l.src) && isExprDigit(l.src[l.pos+1]) {
			seenDot = true
			l.pos++
			continue
		}
		break
	}
	return exprToken{kind: exprTokNumber, start: start, end: l.pos, text: l.src[start:l.pos]}
}

// exprParser is a recursive-descent parser over one capture. It stops at
// the first syntax error and records every call site it passes.
type exprParser struct {
	err   *exprError
	calls []exprCall
	lex   exprLexer
	tok   exprToken
}

// parseExpression parses a capture under the §5.4 grammar and returns
// its call sites, or the first syntax error.
//
// The grammar, verbatim from §5.4 (EBNF repeats its nonterminals by
// design):
//
//	expr    := or
//	or      := and ( "||" and )*
//	and     := unary ( "&&" unary )*
//	unary   := "!" unary | cmp
//	cmp     := term ( ( "==" | "!=" | "<=" | ">=" | "<" | ">" ) term )?
//	         | term "in" term
//	term    := INT | FLOAT | STRING | "true" | "false" | list
//	         | "this" postfix*
//	         | call
//	         | "(" expr ")"
//	postfix := "." IDENT "(" args ")"
//	call    := IDENT ( "." IDENT )* "(" args ")"
//	args    := ( expr ( "," expr )* )?
//	list    := "[" ( term ( "," term )* )? "]"
//
// Lexically, INT and FLOAT are decimal literals with an optional leading
// `-` (`12`, `-3`, `2.5`), STRING is a quoted literal with backslash
// escapes, and identifiers are proto identifiers — the forms the
// reference engine's lexer accepts.
//
//nolint:dupword // EBNF: `term ( … term )` and `term "in" term` are the grammar.
func parseExpression(src string) ([]exprCall, *exprError) {
	p := &exprParser{lex: exprLexer{src: src}}
	p.advance()
	p.parseOr()
	if p.err == nil && p.tok.kind != exprTokEOF {
		if p.isCmpOp() || p.isKeyword("in") {
			p.failf(p.tok, "comparison operators do not chain: `a < b < c` is not an expression")
		} else {
			p.failf(p.tok, "unexpected %s after the expression", p.describe(p.tok))
		}
	}
	if p.err != nil {
		return nil, p.err
	}
	return p.calls, nil
}

func (p *exprParser) advance() {
	if p.err != nil {
		return
	}
	tok, err := p.lex.next()
	if err != nil {
		p.err = err
		p.tok = exprToken{kind: exprTokEOF, start: err.start, end: err.end}
		return
	}
	p.tok = tok
}

func (p *exprParser) failf(tok exprToken, format string, args ...any) {
	if p.err == nil {
		p.err = &exprError{msg: fmt.Sprintf(format, args...), start: tok.start, end: tok.end}
	}
}

func (p *exprParser) describe(tok exprToken) string {
	if tok.kind == exprTokEOF {
		return "the end of the expression"
	}
	return "`" + tok.text + "`"
}

func (p *exprParser) isPunct(text string) bool {
	return p.tok.kind == exprTokPunct && p.tok.text == text
}

func (p *exprParser) isKeyword(text string) bool {
	return p.tok.kind == exprTokIdent && p.tok.text == text
}

func (p *exprParser) isCmpOp() bool {
	if p.tok.kind != exprTokPunct {
		return false
	}
	switch p.tok.text {
	case "==", "!=", "<=", ">=", "<", ">":
		return true
	}
	return false
}

func (p *exprParser) parseOr() {
	p.parseAnd()
	for p.err == nil && p.isPunct("||") {
		p.advance()
		p.parseAnd()
	}
}

func (p *exprParser) parseAnd() {
	p.parseUnary()
	for p.err == nil && p.isPunct("&&") {
		p.advance()
		p.parseUnary()
	}
}

func (p *exprParser) parseUnary() {
	if p.err != nil {
		return
	}
	if p.isPunct("!") {
		p.advance()
		p.parseUnary()
		return
	}
	p.parseCmp()
}

func (p *exprParser) parseCmp() {
	p.parseTerm()
	if p.err != nil {
		return
	}
	switch {
	case p.isCmpOp(), p.isKeyword("in"):
		p.advance()
		p.parseTerm()
	}
}

func (p *exprParser) parseTerm() {
	if p.err != nil {
		return
	}
	tok := p.tok
	switch {
	case tok.kind == exprTokNumber || tok.kind == exprTokString:
		p.advance()
	case tok.kind == exprTokPunct && tok.text == "(":
		p.advance()
		p.parseOr()
		if p.err == nil && !p.isPunct(")") {
			p.failf(p.tok, "expected `)` to close the parenthesis opened at offset %d, got %s", tok.start, p.describe(p.tok))
			return
		}
		p.advance()
	case tok.kind == exprTokPunct && tok.text == "[":
		p.parseList()
	case tok.kind == exprTokIdent && (tok.text == "true" || tok.text == "false"):
		p.advance()
	case tok.kind == exprTokIdent && tok.text == "this":
		p.advance()
		p.parsePostfix()
	case tok.kind == exprTokIdent && tok.text == "in":
		p.failf(tok, "expected an expression, got `in`")
	case tok.kind == exprTokIdent:
		p.parseCall()
	default:
		p.failf(tok, "expected an expression, got %s", p.describe(tok))
	}
}

// parsePostfix parses the `.m(args)` chain after `this`. A `.` that is
// not followed by a call is field selection, which the language does
// not have.
func (p *exprParser) parsePostfix() {
	for p.err == nil && p.isPunct(".") {
		p.advance()
		name := p.tok
		if name.kind != exprTokIdent {
			p.failf(name, "expected a method name after `.`, got %s", p.describe(name))
			return
		}
		p.advance()
		if !p.isPunct("(") {
			p.failf(name, "field selection is not part of the expression language (`this.%s`); "+
				"a rule over a message's fields is a declared function called with `this`", name.text)
			return
		}
		args, end := p.parseArgs()
		if p.err != nil {
			return
		}
		p.calls = append(p.calls, exprCall{
			name: name.text, nameStart: name.start, nameEnd: name.end,
			start: name.start, end: end, args: args, method: true,
		})
	}
}

// parseCall parses `IDENT ("." IDENT)* "(" args ")"` starting at the
// current identifier.
func (p *exprParser) parseCall() {
	first := p.tok
	last := first
	p.advance()
	for p.err == nil && p.isPunct(".") {
		p.advance()
		if p.tok.kind != exprTokIdent {
			p.failf(p.tok, "expected an identifier after `.`, got %s", p.describe(p.tok))
			return
		}
		last = p.tok
		p.advance()
	}
	if p.err != nil {
		return
	}
	name := p.lex.src[first.start:last.end]
	if !p.isPunct("(") {
		p.failf(exprToken{start: first.start, end: last.end},
			"`%s` is not bound — only `this` is; a call is written `%s(…)`", name, name)
		return
	}
	args, end := p.parseArgs()
	if p.err != nil {
		return
	}
	p.calls = append(p.calls, exprCall{
		name: name, nameStart: first.start, nameEnd: last.end,
		start: first.start, end: end, args: args,
	})
}

// parseArgs parses `"(" args ")"` at the current `(` and returns the
// argument count and the offset just past the closing parenthesis.
func (p *exprParser) parseArgs() (int, int) {
	open := p.tok
	p.advance()
	if p.isPunct(")") {
		end := p.tok.end
		p.advance()
		return 0, end
	}
	count := 0
	for p.err == nil {
		p.parseOr()
		if p.err != nil {
			return 0, 0
		}
		count++
		switch {
		case p.isPunct(","):
			p.advance()
		case p.isPunct(")"):
			end := p.tok.end
			p.advance()
			return count, end
		default:
			p.failf(p.tok, "expected `,` or `)` in the argument list opened at offset %d, got %s", open.start, p.describe(p.tok))
			return 0, 0
		}
	}
	return 0, 0
}

// parseList parses `"[" ( term ("," term)* )? "]"` at the current `[`.
func (p *exprParser) parseList() {
	open := p.tok
	p.advance()
	if p.isPunct("]") {
		p.advance()
		return
	}
	for p.err == nil {
		p.parseTerm()
		if p.err != nil {
			return
		}
		switch {
		case p.isPunct(","):
			p.advance()
		case p.isPunct("]"):
			p.advance()
			return
		default:
			p.failf(p.tok, "expected `,` or `]` in the list opened at offset %d, got %s", open.start, p.describe(p.tok))
			return
		}
	}
}
