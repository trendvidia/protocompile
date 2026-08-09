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
	"github.com/trendvidia/protocompile/report"
	"github.com/trendvidia/protocompile/seq"
	"github.com/trendvidia/protocompile/token"
)

// canonicalHTTPFQN is the canonical `@http` annotation declared in
// protowire/schema/v1/annotations.proto. The §5.2 routing checks key
// on this resolved FQN, so a user annotation that happens to be named
// `http` is unaffected.
const canonicalHTTPFQN FullName = "protowire.schema.v1.http"

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
				target := u.Target()
				if target.IsZero() || target.FullName() != canonicalHTTPFQN {
					continue
				}
				validateHTTPUse(r, m, u)
			}
		}
	}
}

// validateHTTPUse checks one `@http` use site: a non-empty verb, an
// absolute path, well-formed `{name}` template segments, and a
// same-named top-level field on the request message for each.
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
		if hasMemberNamed(input, name) {
			continue
		}
		r.Errorf("`@http` path template `{%s}` binds no field of `%s`", name, input.FullName()).Apply(
			report.Snippet(span),
			report.Snippetf(m.AST().Name(), "on this method"),
			report.Notef("each `{name}` segment binds to the same-named top-level field "+
				"of the request message (RFC-001 §5.2); a segment that binds nothing "+
				"makes the route unservable"),
		)
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
		value := b.Arg.Value()
		if value.Kind() != ast.ExprKindLiteral {
			return "", value, false
		}
		tok := value.AsLiteral().Token
		if tok.Kind() != token.String {
			return "", value, false
		}
		return tok.AsString().Text(), value, true
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

// hasMemberNamed reports whether ty declares a top-level member of
// that name.
func hasMemberNamed(ty Type, name string) bool {
	for member := range seq.Values(ty.Members()) {
		if member.Name() == name {
			return true
		}
	}
	return false
}
