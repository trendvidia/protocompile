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
	"github.com/trendvidia/protocompile/report"
	"github.com/trendvidia/protocompile/seq"
	"github.com/trendvidia/protocompile/source"
)

// legalizeAnnotationPlacement enforces RFC-001 §5.1 hybrid placement,
// which is fixed by production:
//
//   - Trailing (between the declaration and its `;`): `type`, field,
//     `function`, enum value.
//   - Leading (before the declaration): `message`, `enum`, `service`,
//     `rpc`, `oneof`.
//
// The parser accepts `@name(args)` runs in both positions on every
// declaration and attaches them to the right carrier either way; this
// pass diagnoses the out-of-production position. Block-bodied
// declarations can never receive trailing annotations (there is no
// `;` to precede), so only the leading-on-trailing-production and
// trailing-on-`rpc` cases can occur.
//
// Productions the spec's placement list does not mention —
// `annotation` declarations (which take trailing annotations in this
// implementation), `extend`, `group`, options — are left unchecked.
func legalizeAnnotationPlacement(p *parser, def ast.DeclDef) {
	anns := def.Annotations()
	if anns.Len() == 0 {
		return
	}

	var wantTrailing bool
	var what string
	switch def.Classify() {
	case ast.DefKindField:
		wantTrailing, what = true, "a field"
	case ast.DefKindEnumValue:
		wantTrailing, what = true, "an enum value"
	case ast.DefKindMethod:
		wantTrailing, what = false, "a method"
	case ast.DefKindMessage, ast.DefKindEnum, ast.DefKindService, ast.DefKindOneof:
		// Leading-only, but these are block-bodied: the parser cannot
		// produce trailing annotations for them.
		return
	default:
		return
	}

	// The def's own span excludes attached annotations, so position
	// distinguishes leading from trailing.
	anchor := def.Span().Start
	for ann := range seq.Values(anns) {
		leading := ann.Span().Start < anchor
		if leading == wantTrailing {
			position, want := "leading", "after it, before the `;`"
			if !leading {
				position, want = "trailing", "before it"
			}
			p.Errorf("%s annotation on %s", position, what).Apply(
				report.Snippet(ann),
				report.Notef("annotations on %s are written %s (RFC-001 §5.1 hybrid placement)", what, want),
			)
		}
	}
}

// legalizeLeadingOnTrailingDecl diagnoses leading annotation runs on
// the trailing-placement declarations that are not defs: `type` and
// `function`.
func legalizeLeadingOnTrailingDecl(p *parser, what string, keyword source.Spanner, anns seq.Indexer[ast.DeclAnnotationUse]) {
	anchor := keyword.Span().Start
	for ann := range seq.Values(anns) {
		if ann.Span().Start < anchor {
			p.Errorf("leading annotation on a %s declaration", what).Apply(
				report.Snippet(ann),
				report.Notef("annotations on a %s declaration are written after it, "+
					"before the `;` (RFC-001 §5.1 hybrid placement)", what),
			)
		}
	}
}
