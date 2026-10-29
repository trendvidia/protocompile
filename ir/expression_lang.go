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

	"github.com/trendvidia/protocompile/ast/predeclared"
	"github.com/trendvidia/protocompile/report"
	"github.com/trendvidia/protocompile/source"
)

// This file checks expression-classified annotation arguments against
// the RFC-001 §5.4 expression language — the one language every
// `@validate` rule is written in (protowire#282). The compiler's
// obligations there: reject a capture that is not an expression, a call
// to a name that is neither a visible `function` declaration nor a §5.4
// builtin, a builtin called with the wrong arity, and a rule that is
// visibly not a boolean (protowire#437). Declared-function arity is
// [verifyCallArities]'s job; every other operand kind is checked by the
// engine at evaluation, not here.
//
// The grammar is documented on [parseExpression].

// exprBuiltin describes one §5.4 builtin: how many arguments it takes
// beyond its receiver, whether it has a receiver at all, and what it
// returns.
type exprBuiltin struct {
	args     int
	receiver bool
	result   exprKind
}

// exprBuiltins is the complete §5.4 builtin set. A receiver builtin is
// callable in method form (`this.size()`) and in function form with the
// receiver first (`size(this)`); `now()` has no receiver.
var exprBuiltins = map[string]exprBuiltin{
	"size":        {args: 0, receiver: true, result: exprNumber},
	"starts_with": {args: 1, receiver: true, result: exprBool},
	"ends_with":   {args: 1, receiver: true, result: exprBool},
	"contains":    {args: 1, receiver: true, result: exprBool},
	"matches":     {args: 1, receiver: true, result: exprBool},
	"now":         {args: 0, receiver: false, result: exprTimestamp},
	"is_nan":      {args: 0, receiver: true, result: exprBool},
	"defined":     {args: 0, receiver: true, result: exprBool},
	"unique":      {args: 0, receiver: true, result: exprBool},
}

// exprBuiltinList renders the builtin set for diagnostics, in a fixed
// order.
const exprBuiltinList = "size, starts_with, ends_with, contains, matches, now, is_nan, defined, unique"

// validateExpressionLanguage parses an expression-classified argument's
// capture under the §5.4 grammar and diagnoses what the compiler owns:
// syntax, name resolution against declared functions and builtins,
// builtin arity, and a rule that is visibly not a boolean. Declared-
// function call sites are the ones [exprSite.calls] resolved; every
// other call must be a builtin.
//
// subject names the capture in diagnostics. this is what `this` binds
// to, zero where it is not known (a default, a message-level rule), in
// which case `this` alone is never refused.
func validateExpressionLanguage(r *report.Report, site exprSite, subject string, this thisBinding) {
	span := site.span()
	if span.IsZero() {
		return
	}
	text := span.Text()
	sub := func(start, end int) source.Span {
		return span.File.Span(span.Start+start, span.Start+end)
	}

	analysis, perr := analyzeExpression(text)
	if perr != nil {
		r.Errorf("%s is not an expression: %s", subject, perr.msg).Apply(
			report.Snippet(sub(perr.start, perr.end)),
			report.Notef("RFC-001 §5.4: comparisons, `in`, `&&` `||` `!`, literals, `this`, "+
				"enum value names, builtin methods on `this`, and declared-function calls"),
		)
		return
	}

	declared := make(map[int]bool)
	for _, call := range site.calls() {
		declared[call.Span.Start] = true
	}
	isDeclared := func(call exprCall) bool { return !call.method && declared[span.Start+call.start] }
	for _, call := range analysis.calls {
		if isDeclared(call) {
			continue // A declared function: arity is verifyCallArities's.
		}
		builtin, ok := exprBuiltins[call.name]
		switch {
		case !ok && call.method:
			r.Errorf("unknown method `%s` on `this` in %s", call.name, subject).Apply(
				report.Snippet(sub(call.nameStart, call.nameEnd)),
				report.Helpf("only builtins are called on `this` (%s); a declared function is called as `%s(this, …)`",
					exprBuiltinList, call.name),
			)
		case !ok:
			r.Errorf("unknown function `%s` in %s: neither a visible `function` declaration nor a builtin",
				call.name, subject,
			).Apply(
				report.Snippet(sub(call.nameStart, call.nameEnd)),
				report.Helpf("builtins: %s", exprBuiltinList),
			)
		default:
			checkBuiltinArity(r, call, builtin, sub)
		}
	}

	resolved := true
	for _, ref := range analysis.enums {
		at := sub(ref.start, ref.end)
		if !resolveEnumValueName(site.file, nil, site.scope, FullName(ref.name), at).IsZero() {
			continue
		}
		resolved = false
		r.Errorf("`%s` is not bound and is not an enum value, in %s", ref.name, subject).Apply(
			report.Snippet(at),
			report.Helpf("only `this` is bound; an enum value is named `VALUE`, `pkg.VALUE` or `Enum.VALUE`, "+
				"and a call is written `%s(…)`", ref.name),
		)
	}
	if !resolved {
		return // An unresolved name is not also a type error.
	}

	for _, need := range analysis.needs {
		kind := need.val.kind
		switch kind {
		case exprCallResult:
			call := analysis.calls[need.val.call]
			if isDeclared(call) {
				continue // A declared function is a predicate (§6.5).
			}
			builtin, ok := exprBuiltins[call.name]
			if !ok || builtin.result == exprBool {
				continue
			}
			kind = builtin.result
		case exprThis:
			if !thisIsNotBool(this) {
				continue
			}
		}
		reportNotBoolean(r, need, kind, subject, this, sub(need.val.start, need.val.end))
	}
}

// thisIsNotBool reports whether `this` is visibly not a boolean: a
// collection, an enum, or a scalar other than bool. A message may be a
// wrapper that binds as its value (§6.2), and an unknown binding is
// unknown, so neither is refused.
func thisIsNotBool(this thisBinding) bool {
	switch {
	case this.collection != "":
		return true
	case this.ty.IsZero():
		return false
	case this.ty.IsEnum():
		return true
	case this.ty.IsPredeclared():
		return this.ty.Predeclared() != predeclared.Bool
	}
	return false
}

// describe names what `this` is for a diagnostic.
func (b thisBinding) describe() string {
	if b.collection != "" {
		return b.collection
	}
	return fmt.Sprintf("a `%s`", b.ty.FullName())
}

// reportNotBoolean diagnoses a subexpression in a boolean position that
// is visibly something else (RFC-001 §5.4, protowire#437).
func reportNotBoolean(r *report.Report, need exprNeed, kind exprKind, subject string, this thisBinding, at source.Span) {
	what := kind.describe()
	if kind == exprThis {
		what = "`this`, " + this.describe() + " here"
	}
	d := r.Errorf("%s is %s, not a boolean, in %s", need.role, what, subject).Apply(report.Snippet(at))
	if kind == exprString && need.role == ruleResult {
		d.Apply(report.Helpf("a quoted rule is a string literal; write the rule without the quotes"))
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

// exprKind is what a term visibly evaluates to, as far as its syntax
// shows. exprUnknown is a term whose kind depends on a binding: a
// declared function's result, or `this`.
type exprKind uint8

const (
	exprUnknown exprKind = iota
	exprBool
	exprNumber
	exprString
	exprList
	exprEnumValue
	exprTimestamp
	exprThis // `this` alone: boolean exactly when the carrier is.
	exprCallResult
)

// describe names a kind for a diagnostic.
func (k exprKind) describe() string {
	switch k {
	case exprNumber:
		return "a number"
	case exprString:
		return "a string"
	case exprList:
		return "a list"
	case exprEnumValue:
		return "an enum value"
	case exprTimestamp:
		return "a timestamp"
	}
	return "a value"
}

// exprVal is one parsed subexpression: its kind, its byte range, and,
// for a call, which of the parser's calls it is.
type exprVal struct {
	kind       exprKind
	start, end int
	call       int
}

// exprNeed is a subexpression in a position that must be a boolean: the
// whole rule, or an operand of `!`, `&&` or `||`. role names the
// position for a diagnostic.
type exprNeed struct {
	val  exprVal
	role string
}

// ruleResult is the role of the whole expression: a rule is a boolean.
const ruleResult = "the rule's result"

// exprEnumRef is one enum value reference the parser found (RFC-001
// §5.4 `enumref`): an identifier run not followed by `(`.
type exprEnumRef struct {
	name       string // As written: `MODE_MATCH`, `Mode.MODE_MATCH`, `pkg.MODE_MATCH`.
	start, end int
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
// the first syntax error and records every call site and enum value
// reference it passes.
type exprParser struct {
	err   *exprError
	calls []exprCall
	enums []exprEnumRef
	needs []exprNeed
	lex   exprLexer
	tok   exprToken
	prev  exprToken // The token before tok, for subexpression ends.
}

// exprAnalysis is everything [analyzeExpression] reads off a capture.
type exprAnalysis struct {
	calls []exprCall
	enums []exprEnumRef
	// needs are the subexpressions that must be booleans and whose kind
	// is not visibly boolean, in source order.
	needs []exprNeed
}

// parseExpression parses a capture under the §5.4 grammar and returns
// its call sites and enum value references, or the first syntax error.
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
//	         | enumref
//	         | "(" expr ")"
//	postfix := "." IDENT "(" args ")"
//	call    := IDENT ( "." IDENT )* "(" args ")"
//	enumref := IDENT ( "." IDENT )*        not followed by "("
//	args    := ( expr ( "," expr )* )?
//	list    := "[" ( term ( "," term )* )? "]"
//
// Lexically, INT and FLOAT are decimal literals with an optional leading
// `-` (`12`, `-3`, `2.5`), STRING is a quoted literal with backslash
// escapes, and identifiers are proto identifiers — the forms the
// reference engine's lexer accepts.
//
//nolint:dupword // EBNF: `term ( … term )` and `term "in" term` are the grammar.
func parseExpression(src string) ([]exprCall, []exprEnumRef, *exprError) {
	a, err := analyzeExpression(src)
	return a.calls, a.enums, err
}

// analyzeExpression is [parseExpression] that also records every
// position that must be a boolean but is not visibly one.
func analyzeExpression(src string) (exprAnalysis, *exprError) {
	p := &exprParser{lex: exprLexer{src: src}}
	p.advance()
	p.need(p.parseOr(), ruleResult)
	if p.err == nil && p.tok.kind != exprTokEOF {
		if p.isCmpOp() || p.isKeyword("in") {
			p.failf(p.tok, "comparison operators do not chain: `a < b < c` is not an expression")
		} else {
			p.failf(p.tok, "unexpected %s after the expression", p.describe(p.tok))
		}
	}
	if p.err != nil {
		return exprAnalysis{}, p.err
	}
	return exprAnalysis{calls: p.calls, enums: p.enums, needs: p.needs}, nil
}

// need records v, in a position that must be a boolean, unless it
// visibly is one.
func (p *exprParser) need(v exprVal, role string) {
	if p.err != nil || v.kind == exprBool || v.kind == exprUnknown {
		return
	}
	p.needs = append(p.needs, exprNeed{val: v, role: role})
}

// boolean is the result of an operator that yields a boolean, spanning
// start to the last token consumed.
func (p *exprParser) boolean(start int) exprVal {
	return exprVal{kind: exprBool, start: start, end: p.prev.end}
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
	p.prev = p.tok
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

func (p *exprParser) parseOr() exprVal {
	return p.parseLogical("||", p.parseAnd)
}

func (p *exprParser) parseAnd() exprVal {
	return p.parseLogical("&&", p.parseUnary)
}

// parseLogical parses `operand (op operand)*`. With an operator, every
// operand must be a boolean and so is the result.
func (p *exprParser) parseLogical(op string, operand func() exprVal) exprVal {
	first := operand()
	if p.err != nil || !p.isPunct(op) {
		return first
	}
	role := "an operand of `" + op + "`"
	p.need(first, role)
	for p.err == nil && p.isPunct(op) {
		p.advance()
		p.need(operand(), role)
	}
	return p.boolean(first.start)
}

func (p *exprParser) parseUnary() exprVal {
	if p.err != nil {
		return exprVal{}
	}
	if p.isPunct("!") {
		start := p.tok.start
		p.advance()
		p.need(p.parseUnary(), "the operand of `!`")
		return p.boolean(start)
	}
	return p.parseCmp()
}

func (p *exprParser) parseCmp() exprVal {
	left := p.parseTerm()
	if p.err != nil {
		return exprVal{}
	}
	switch {
	case p.isCmpOp(), p.isKeyword("in"):
		p.advance()
		p.parseTerm()
		return p.boolean(left.start)
	}
	return left
}

func (p *exprParser) parseTerm() exprVal {
	if p.err != nil {
		return exprVal{}
	}
	tok := p.tok
	term := func(kind exprKind) exprVal {
		return exprVal{kind: kind, start: tok.start, end: p.prev.end}
	}
	switch {
	case tok.kind == exprTokNumber:
		p.advance()
		return term(exprNumber)
	case tok.kind == exprTokString:
		p.advance()
		return term(exprString)
	case tok.kind == exprTokPunct && tok.text == "(":
		p.advance()
		inner := p.parseOr()
		if p.err == nil && !p.isPunct(")") {
			p.failf(p.tok, "expected `)` to close the parenthesis opened at offset %d, got %s", tok.start, p.describe(p.tok))
			return exprVal{}
		}
		p.advance()
		inner.start, inner.end = tok.start, p.prev.end
		return inner
	case tok.kind == exprTokPunct && tok.text == "[":
		p.parseList()
		return term(exprList)
	case tok.kind == exprTokIdent && (tok.text == "true" || tok.text == "false"):
		p.advance()
		return term(exprBool)
	case tok.kind == exprTokIdent && tok.text == "this":
		p.advance()
		if v, ok := p.parsePostfix(); ok {
			v.start = tok.start
			return v
		}
		return term(exprThis)
	case tok.kind == exprTokIdent && tok.text == "in":
		p.failf(tok, "expected an expression, got `in`")
	case tok.kind == exprTokIdent:
		return p.parseCall()
	default:
		p.failf(tok, "expected an expression, got %s", p.describe(tok))
	}
	return exprVal{}
}

// parsePostfix parses the `.m(args)` chain after `this` and returns the
// last call, reporting false when there is none. A `.` that is not
// followed by a call is field selection, which the language does not
// have.
func (p *exprParser) parsePostfix() (exprVal, bool) {
	var last exprVal
	found := false
	for p.err == nil && p.isPunct(".") {
		p.advance()
		name := p.tok
		if name.kind != exprTokIdent {
			p.failf(name, "expected a method name after `.`, got %s", p.describe(name))
			return exprVal{}, false
		}
		p.advance()
		if !p.isPunct("(") {
			p.failf(name, "field selection is not part of the expression language (`this.%s`); "+
				"a rule over a message's fields is a declared function called with `this`", name.text)
			return exprVal{}, false
		}
		args, end := p.parseArgs()
		if p.err != nil {
			return exprVal{}, false
		}
		p.calls = append(p.calls, exprCall{
			name: name.text, nameStart: name.start, nameEnd: name.end,
			start: name.start, end: end, args: args, method: true,
		})
		last = exprVal{kind: exprCallResult, end: end, call: len(p.calls) - 1}
		found = true
	}
	return last, found
}

// parseCall parses `IDENT ("." IDENT)* "(" args ")"` starting at the
// current identifier, or the same run without the parenthesis, which is
// an enum value reference.
func (p *exprParser) parseCall() exprVal {
	first := p.tok
	last := first
	parts := []string{first.text}
	p.advance()
	for p.err == nil && p.isPunct(".") {
		p.advance()
		if p.tok.kind != exprTokIdent {
			p.failf(p.tok, "expected an identifier after `.`, got %s", p.describe(p.tok))
			return exprVal{}
		}
		last = p.tok
		parts = append(parts, last.text)
		p.advance()
	}
	if p.err != nil {
		return exprVal{}
	}
	name := p.lex.src[first.start:last.end]
	if !p.isPunct("(") {
		// The spelling is canonical (`Mode.X`, whatever the spacing), so an
		// engine re-parsing the source finds it in Expression.enum_values.
		p.enums = append(p.enums, exprEnumRef{name: strings.Join(parts, "."), start: first.start, end: last.end})
		return exprVal{kind: exprEnumValue, start: first.start, end: last.end}
	}
	args, end := p.parseArgs()
	if p.err != nil {
		return exprVal{}
	}
	p.calls = append(p.calls, exprCall{
		name: name, nameStart: first.start, nameEnd: last.end,
		start: first.start, end: end, args: args,
	})
	return exprVal{kind: exprCallResult, start: first.start, end: end, call: len(p.calls) - 1}
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
