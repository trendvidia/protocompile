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

package fdp

import (
	"strings"

	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/types/descriptorpb"

	pwsv1 "github.com/trendvidia/protocompile/gen/protowire/schema/v1"
	"github.com/trendvidia/protocompile/internal"
)

// canonicalHTTPFQN is the canonical `@http` annotation declared in
// protowire/schema/v1/annotations.proto, as its use sites appear in
// [pwsv1.Annotation.Name] after lowering. Keying on the resolved FQN
// leaves a user annotation that happens to be named `http` alone.
//
// The IR's §5.2 routing checks key on the same constant; see
// [internal.CanonicalHTTPFQN] for why it lives outside both.
const canonicalHTTPFQN = internal.CanonicalHTTPFQN

// httpRuleFor lowers the canonical `@http` use sites of one method's
// annotation carrier into a standard `google.api.HttpRule`, or returns
// nil when the carrier holds none.
//
// RFC-001 §5.2: the carrier keeps the whole enriched operation surface
// (`summary`, `operation_id`, `tags`, `security`), which `HttpRule` has
// no place for; the standard extension carries the routing skeleton
// every off-the-shelf REST binder reads — connect vanguard, grpc-
// gateway, Envoy's grpc_json_transcoder, buf's OpenAPI plugins
// (protowire#213). Both are emitted; neither replaces the other.
//
// A method carrying several `@http` use sites lowers the first as the
// primary rule and the rest as `additional_bindings`, in source order.
//
// The read is deliberately of the lowered carrier rather than of the
// IR use site: the routing skeleton a binder acts on and the metadata
// a renderer reads then come from one parse, and cannot disagree.
func httpRuleFor(list *pwsv1.AnnotationList) *annotations.HttpRule {
	var primary *annotations.HttpRule
	for _, entry := range list.GetEntries() {
		if entry.GetName() != canonicalHTTPFQN {
			continue
		}
		rule := httpRuleFromAnnotation(entry)
		if rule == nil {
			continue
		}
		if primary == nil {
			primary = rule
			continue
		}
		primary.AdditionalBindings = append(primary.AdditionalBindings, rule)
	}
	return primary
}

// httpRuleFromAnnotation builds the rule for one `@http` use site.
// Returns nil when the routing skeleton is not both present and
// string-shaped — the §5.2 checks in the IR pass already diagnosed
// that, and guessing a route here would be worse than emitting none.
func httpRuleFromAnnotation(ann *pwsv1.Annotation) *annotations.HttpRule {
	// The verb is trimmed before it is matched, the same way the IR
	// check trims before testing it for emptiness: otherwise a padded
	// `" GET "` clears that check and lands in the custom-pattern
	// branch here, registering an unreachable verb. The path is *not*
	// trimmed — the IR requires it to start with `/` verbatim, so
	// trimming here would emit a route the IR rejected.
	method := strings.ToUpper(strings.TrimSpace(httpStringArg(ann, "method", 0)))
	path := httpStringArg(ann, "path", 1)
	if method == "" || path == "" {
		return nil
	}

	rule := new(annotations.HttpRule)
	switch method {
	case "GET":
		rule.Pattern = &annotations.HttpRule_Get{Get: path}
	case "PUT":
		rule.Pattern = &annotations.HttpRule_Put{Put: path}
	case "POST":
		rule.Pattern = &annotations.HttpRule_Post{Post: path}
	case "DELETE":
		rule.Pattern = &annotations.HttpRule_Delete{Delete: path}
	case "PATCH":
		rule.Pattern = &annotations.HttpRule_Patch{Patch: path}
	default:
		// Everything else is a custom pattern: HttpRule names only the
		// five verbs above, and §5.2 constrains `method` to no vocabulary.
		rule.Pattern = &annotations.HttpRule_Custom{
			Custom: &annotations.CustomHttpPattern{Kind: method, Path: path},
		}
	}

	// §5.2 binding: request fields the path template did not bind go to
	// the query string for bodyless methods, to the body otherwise —
	// which is exactly what `body: "*"` means to an HttpRule consumer
	// (every field not bound by the path template). `@http` has no
	// parameter for naming a single body field, so `*` is the only form
	// the annotation can produce.
	if !bodylessHTTPMethod(method) {
		rule.Body = "*"
	}
	return rule
}

// bodylessHTTPMethod reports whether the §5.2 binding rules send the
// unbound request fields to the query string rather than to the body.
// The set is the one the OpenAPI renderer splits on, so the rendered
// spec and the emitted rule describe the same request.
func bodylessHTTPMethod(method string) bool {
	switch method {
	case "GET", "HEAD", "DELETE", "OPTIONS":
		return true
	}
	return false
}

// httpStringArg resolves a string-valued annotation argument against
// the declared parameter list the way §5.1 use sites bind: a named
// argument matches by name, a positional one by its declared position.
// Returns "" when the argument is absent or not string-shaped.
func httpStringArg(ann *pwsv1.Annotation, param string, pos int) string {
	positional := 0
	for _, a := range ann.GetArgs() {
		switch a.GetName() {
		case param:
			return a.GetStringValue()
		case "":
			if positional == pos {
				return a.GetStringValue()
			}
			positional++
		}
	}
	return ""
}

// hasAuthoredHTTPRule reports whether the method's options already
// carry an author-written `(google.api.http)`, in which case the
// lowering leaves the method alone rather than emitting a second,
// competing rule.
//
// User-written options are serialised into unknown-field bytes by
// [generator.options], so the check scans the raw bytes for the field
// number instead of asking for the extension.
func hasAuthoredHTTPRule(opts *descriptorpb.MethodOptions) bool {
	want := annotations.E_Http.TypeDescriptor().Number()
	b := opts.ProtoReflect().GetUnknown()
	for len(b) > 0 {
		num, kind, n := protowire.ConsumeTag(b)
		if n < 0 {
			return false
		}
		b = b[n:]
		m := protowire.ConsumeFieldValue(num, kind, b)
		if m < 0 {
			return false
		}
		b = b[m:]
		if num == want {
			return true
		}
	}
	return false
}
