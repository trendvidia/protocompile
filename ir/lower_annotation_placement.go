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
	"github.com/trendvidia/protocompile/report"
	"github.com/trendvidia/protocompile/seq"
)

const (
	canonicalRequiredFQN FullName = "protowire.schema.v1.required"
	canonicalDefaultFQN  FullName = "protowire.schema.v1.default"
)

// literalMessages are the message types a PXF literal can denote, besides
// the pxf arbitrary-precision types (draft -01, "Default Placement").
var literalMessages = map[FullName]bool{
	"google.protobuf.Timestamp":   true,
	"google.protobuf.Duration":    true,
	"google.protobuf.BoolValue":   true,
	"google.protobuf.BytesValue":  true,
	"google.protobuf.DoubleValue": true,
	"google.protobuf.FloatValue":  true,
	"google.protobuf.Int32Value":  true,
	"google.protobuf.Int64Value":  true,
	"google.protobuf.StringValue": true,
	"google.protobuf.UInt32Value": true,
	"google.protobuf.UInt64Value": true,
}

// validateAnnotationPlacement enforces the places RFC-001 §5.2 says the
// canonical `@required` and `@default` are not valid. Each of these
// otherwise compiles and lowers, and no engine can act on it.
//
// It runs after [propagateTypeAliasAnnotations], so a rule that arrives
// through a type alias is checked on the field it expanded onto. Checks key
// on the resolved FQN, so a user annotation named `default` is unaffected.
func validateAnnotationPlacement(file *File, r *report.Report) {
	for ty := range seq.Values(file.AllTypes()) {
		if ty.IsMapEntry() {
			continue // See allAnnotationUses.
		}
		kind := "a message"
		if ty.IsEnum() {
			kind = "an enum"
		}
		rejectDefault(r, ty.Annotations(), kind)
		for m := range seq.Values(ty.Members()) {
			validateMemberPlacement(r, m)
		}
		for o := range seq.Values(ty.Oneofs()) {
			validateOneofPlacement(r, o)
		}
	}
	for ext := range seq.Values(file.AllExtensions()) {
		validateMemberPlacement(r, ext)
	}
	for svc := range seq.Values(file.Services()) {
		rejectDefault(r, svc.Annotations(), "a service")
		for m := range seq.Values(svc.Methods()) {
			rejectDefault(r, m.Annotations(), "a method")
		}
	}
}

// rejectDefault reports every canonical `@default` in uses, which sit on a
// declaration of the given kind rather than on a field.
func rejectDefault(r *report.Report, uses seq.Indexer[AnnotationUse], kind string) {
	for u := range seq.Values(uses) {
		if u.Target().FullName() == canonicalDefaultFQN {
			r.Errorf("`@default` is not valid on %s", kind).Apply(
				report.Snippet(u.AST()),
				report.Notef("`@default` populates an absent field, so it attaches only to a field"),
			)
		}
	}
}

// validateMemberPlacement checks one field or enum value.
func validateMemberPlacement(r *report.Report, m Member) {
	if m.IsEnumValue() {
		rejectDefault(r, m.Annotations(), "an enum value")
		return
	}

	for u := range seq.Values(m.Annotations()) {
		switch u.Target().FullName() {
		case canonicalRequiredFQN:
			if !m.Oneof().IsZero() {
				r.Errorf("`@required` is not valid on a oneof member").Apply(
					report.Snippet(u.AST()),
					report.Snippetf(m.Oneof().AST().Name(), "member of this oneof"),
					report.Notef("setting one member clears the others, so requiring "+
						"this member would refuse every other arm"),
					report.Helpf("write a leading `@required` on the oneof to require that a member is set"),
				)
			}
		case canonicalDefaultFQN:
			if what := noLiteralForm(m); what != "" {
				r.Errorf("`@default` is not valid on %s", what).Apply(
					report.Snippet(u.AST()),
					report.Snippetf(m.AST().Name(), "on this field"),
					report.Notef("`@default` carries one literal, and no literal denotes this field"),
				)
			}
		}
	}
}

// noLiteralForm names why a single literal cannot denote m, or returns ""
// when one can.
func noLiteralForm(m Member) string {
	switch {
	case m.IsMap():
		return "a map field"
	case m.IsRepeated():
		return "a repeated field"
	case m.IsGroup():
		return "a group field"
	}
	elem := m.Element()
	if !elem.IsMessage() || literalMessages[elem.FullName()] || elem.PxfNumber() != PxfNone {
		return ""
	}
	return "a field of message type `" + string(elem.FullName()) + "`"
}

// validateOneofPlacement enforces the §5.2 rules on `@default` across the
// members of one oneof.
func validateOneofPlacement(r *report.Report, o Oneof) {
	rejectDefault(r, o.Annotations(), "a oneof")

	var required, first AnnotationUse
	for u := range seq.Values(o.Annotations()) {
		if u.Target().FullName() == canonicalRequiredFQN {
			required = u
		}
	}
	for m := range seq.Values(o.Members()) {
		for u := range seq.Values(m.Annotations()) {
			if u.Target().FullName() != canonicalDefaultFQN {
				continue
			}
			if !required.IsZero() {
				r.Errorf("`@default` on a member of a `@required` oneof").Apply(
					report.Snippet(u.AST()),
					report.Snippetf(required.AST(), "required here"),
					report.Notef("the default says the oneof is never absent, "+
						"and `@required` says that absence is an error"),
				)
			}
			if first.IsZero() {
				first = u
				continue
			}
			r.Errorf("`@default` on more than one member of a oneof").Apply(
				report.Snippet(u.AST()),
				report.Snippetf(first.AST(), "first default here"),
				report.Notef("with no member present, one default would have to win, "+
					"and declaration order must not decide what a document means"),
			)
		}
	}
}
