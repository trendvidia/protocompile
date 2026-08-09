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
	"strings"

	"github.com/trendvidia/protocompile/ast"
	"github.com/trendvidia/protocompile/internal"
	"github.com/trendvidia/protocompile/report"
	"github.com/trendvidia/protocompile/seq"
)

// canonicalHTTPFQN is the canonical `@http` annotation declared in
// protowire/schema/v1/annotations.proto. The §5.2 routing checks key
// on this resolved FQN, so a user annotation that happens to be named
// `http` is unaffected. The `fdp` lowering keys on the same constant;
// see [internal.CanonicalHTTPFQN] for why it lives outside both.
const canonicalHTTPFQN FullName = internal.CanonicalHTTPFQN

// validateHTTPUses checks the routing skeleton of every canonical
// `@http` use site against the method it annotates (RFC-001 §5.2).
//
// The skeleton lowers to a standard `google.api.http` rule, so a
// skeleton that cannot bind is diagnosed here rather than shipped: a
// REST binder given an unbindable rule serves 404 for a route the
// schema and the generated OpenAPI document both promise, and that
// failure is silent at every stage after this one (protowire#213).
//
// Phase B3 validation. Runs after [validateAnnotationUseArgs], whose
// binding pass resolves positional arguments to their declared
// parameters — so both `@http("GET", "/x")` and
// `@http(method = "GET", path = "/x")` are read the same way here.
func validateHTTPUses(file *File, r *report.Report) {
	for svc := range seq.Values(file.Services()) {
		for m := range seq.Values(svc.Methods()) {
			for u := range seq.Values(m.Annotations()) {
				if !isCanonicalHTTPUse(u) {
					continue
				}
				validateHTTPUse(r, m, u)
			}
		}
	}
}

// isCanonicalHTTPUse reports whether a use site resolved to the
// canonical `@http` annotation.
func isCanonicalHTTPUse(u AnnotationUse) bool {
	target := u.Target()
	return !target.IsZero() && target.FullName() == canonicalHTTPFQN
}

// validateHTTPUse checks one `@http` use site: a non-empty verb, an
// absolute path, well-formed `{name}` template segments, and a
// bindable field on the request message for each.
func validateHTTPUse(r *report.Report, m Method, u AnnotationUse) {
	if method, span, ok := httpStringArg(u, "method"); ok && strings.TrimSpace(method) == "" {
		r.Errorf("`@http` method must not be empty").Apply(
			report.Snippet(span),
			report.Notef("the verb selects the `google.api.HttpRule` pattern the route binds under (RFC-001 §5.2)"),
		)
	}

	path, span, ok := httpStringArg(u, "path")
	if !ok {
		// Absent or not string-shaped; the argument-classification pass
		// has already diagnosed the shape.
		return
	}

	if !strings.HasPrefix(path, "/") {
		r.Errorf("`@http` path must be absolute").Apply(
			report.Snippet(span),
			report.Notef("paths start with `/`, as in `/orders/{order_id}`"),
		)
	}

	vars, ok := httpTemplateVars(path)
	if !ok {
		r.Errorf("malformed `@http` path template").Apply(
			report.Snippet(span),
			report.Notef("every `{` in the path opens a template segment and must be closed by a `}`"),
		)
		return
	}

	input, _ := m.Input()
	if input.IsZero() {
		return // Unresolved request type; already diagnosed.
	}
	for _, name := range vars {
		field, repeated := resolveTemplateField(input, name)
		switch {
		case field.IsZero():
			r.Errorf("`@http` path template `{%s}` binds no field of `%s`", name, input.FullName()).Apply(
				report.Snippet(span),
				report.Snippetf(m.AST().Name(), "on this method"),
				report.Notef("each `{name}` segment binds to a field of the request "+
					"message, named from its top level as a dotted path (RFC-001 §5.2); "+
					"a segment that binds nothing makes the route unservable"),
			)

		case !repeated.IsZero():
			r.Errorf("`@http` path template `{%s}` binds the repeated field `%s`",
				name, repeated.FullName(),
			).Apply(
				report.Snippet(span),
				report.Snippetf(m.AST().Name(), "on this method"),
				report.Notef("a path variable expands to a single value, so it cannot "+
					"bind a repeated or map field; `google.api.HttpRule` rejects such a "+
					"binding outright, making the route unservable"),
			)
		}
	}
}

// checkAuthoredHTTPRules warns for every method carrying both an
// `@http` annotation and an author-written `(google.api.http)` option.
//
// A method carries at most one rule at field 72295728, so the fdp
// lowering lets the authored one stand rather than emitting a second,
// competing rule beside it. That leaves the annotation carrier
// advertising one route to an OpenAPI renderer while the wire rule
// binds another — the one way the two spellings of the same route can
// disagree, and silent unless it is said out loud here.
//
// Runs after [resolveOptions], unlike the rest of this file: the check
// reads the method's options, which do not exist until then.
func checkAuthoredHTTPRules(file *File, r *report.Report) {
	for svc := range seq.Values(file.Services()) {
		for m := range seq.Values(svc.Methods()) {
			for u := range seq.Values(m.Annotations()) {
				if !isCanonicalHTTPUse(u) {
					continue
				}
				checkAuthoredHTTPRule(r, m, u)
				break // One warning per method, not per use site.
			}
		}
	}
}

// checkAuthoredHTTPRule warns for one method, blaming u for the
// annotation half of the conflict.
func checkAuthoredHTTPRule(r *report.Report, m Method, u AnnotationUse) {
	for v := range m.Options().Fields() {
		if v.Field().Number() != internal.GoogleAPIHTTPField {
			continue
		}

		options := []report.DiagnosticOption{
			report.Snippetf(u.AST(), "this annotation's route is not lowered"),
			report.Notef("a method carries at most one `google.api.http` rule and the " +
				"author-written one wins; the annotation's path still reaches the " +
				"annotation carrier, so the two describe different routes (RFC-001 §5.2)"),
		}
		if span := v.OptionSpan(); span != nil {
			options = append([]report.DiagnosticOption{
				report.Snippetf(span, "this rule routes the method instead"),
			}, options...)
		}
		r.Warnf("`@http` does not route this method").Apply(options...)
		return
	}
}

// httpStringArg returns the string value bound to a parameter of the
// use site, together with the span to blame for it. Reports false when
// the parameter is unbound or its argument is not a string literal.
func httpStringArg(u AnnotationUse, param string) (string, ast.ExprAny, bool) {
	for _, b := range u.ArgBindings() {
		if b.Param.IsZero() || b.Param.Name() != param {
			continue
		}
		return stringArg(b)
	}
	return "", ast.ExprAny{}, false
}

// httpTemplateVars extracts the variable names of a path's `{...}`
// segments, in order of appearance. A segment may carry a sub-path
// pattern (`{name=segments/**}`); the name is what precedes the `=`.
// Reports false when a `{` is never closed.
func httpTemplateVars(path string) ([]string, bool) {
	var out []string
	for i := 0; i < len(path); {
		open := strings.IndexByte(path[i:], '{')
		if open < 0 {
			break
		}
		open += i
		closing := strings.IndexByte(path[open:], '}')
		if closing < 0 {
			return nil, false
		}
		name := path[open+1 : open+closing]
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			name = name[:eq]
		}
		out = append(out, strings.TrimSpace(name))
		i = open + closing + 1
	}
	return out, true
}

// resolveTemplateField resolves one path-template variable against the
// request message. The variable names a field, either directly or as a
// dotted path descending into nested messages — the form
// `google.api.HttpRule` templates use.
//
// Returns the field the variable binds, plus the first component along
// the way that is repeated or a map (zero when none is). A zero field
// means some component named nothing.
func resolveTemplateField(ty Type, name string) (field, repeated Member) {
	for _, component := range strings.Split(name, ".") {
		if !ty.IsMessage() {
			// Descending through a scalar or an enum: the remaining
			// components name nothing.
			return Member{}, Member{}
		}
		field = ty.MemberByName(component)
		if field.IsZero() {
			return Member{}, Member{}
		}
		if repeated.IsZero() && field.IsRepeated() {
			repeated = field
		}
		ty = field.Element()
	}
	return field, repeated
}
