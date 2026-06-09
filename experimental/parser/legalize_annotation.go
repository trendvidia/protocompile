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
	"github.com/trendvidia/protocompile/experimental/report"
	"github.com/trendvidia/protocompile/experimental/seq"
)

// legalizeAnnotationUse validates a single annotation use site against
// the narrow argument-shape grammar.
//
// The narrow set is intentionally conservative: every shape outside it
// is rejected so the linker and downstream engines see only the
// argument shapes RFC-001 PR 5 was originally scoped to. Widening this
// to the full CEL grammar in a later release is a one-line relaxation
// of the rejection list; narrowing further would be a breaking change.
//
// The accepted shapes are:
//
//   - Literal values: strings, numbers, booleans, etc.
//   - Identifier paths: single or qualified names, optionally with one
//     trailing parenthesised extension component representing a nested
//     call (the parser models calls as path-with-extension).
//   - A `-` prefix applied to a literal (numeric negation).
//
// The rejected shapes are:
//
//   - Array literals (`[ ... ]`).
//   - Dictionary / message literals (`{ ... }`).
//   - Range expressions (`a..b`).
//   - Field expressions (`key: value`).
func legalizeAnnotationUse(p *parser, use ast.DeclAnnotationUse) {
	if use.IsZero() {
		return
	}
	for arg := range seq.Values(use.Args()) {
		legalizeAnnotationArg(p, arg)
	}
}

// legalizeAnnotationArg checks a single argument expression against the
// narrow grammar.
func legalizeAnnotationArg(p *parser, arg ast.ExprAny) {
	switch arg.Kind() {
	case ast.ExprKindInvalid, ast.ExprKindError:
		// Already a parse error; do not double-diagnose.

	case ast.ExprKindLiteral, ast.ExprKindPath:
		// Accepted as-is. Nested extension components in a path
		// represent the call shape (`name(this, "...")`) and are
		// permitted at this layer; downstream legalize can tighten
		// further when call modelling lands.

	case ast.ExprKindPrefixed:
		// Accept only when the prefix wraps a literal (e.g. `-1`).
		// Prefixed expressions wrapping anything more complex are
		// rejected.
		inner := arg.AsPrefixed().Expr()
		if inner.Kind() != ast.ExprKindLiteral {
			rejectAnnotationArg(p, arg)
		}

	default:
		// Array, Dict, Range, Field — outside the narrow grammar.
		rejectAnnotationArg(p, arg)
	}
}

// rejectAnnotationArg emits the canonical "shape not allowed" diagnostic
// for an annotation argument expression.
func rejectAnnotationArg(p *parser, arg ast.ExprAny) {
	p.Errorf("expression shape is not allowed in annotation argument").Apply(
		report.Snippet(arg),
		report.Notef("annotation arguments are restricted to literals, qualified-name references, and a single nested call shape"),
	)
}

// legalizeAttachedAnnotations walks the annotations attached to a
// carrier declaration and validates each.
func legalizeAttachedAnnotations(p *parser, anns interface {
	Len() int
	At(int) ast.DeclAnnotationUse
}) {
	for i := range anns.Len() {
		legalizeAnnotationUse(p, anns.At(i))
	}
}
