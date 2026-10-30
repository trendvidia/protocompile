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

	"github.com/trendvidia/protocompile/internal"
	"github.com/trendvidia/protocompile/report"
	"github.com/trendvidia/protocompile/seq"
)

const (
	canonicalRequiredFQN FullName = "protowire.schema.v1.required"
	canonicalDefaultFQN  FullName = "protowire.schema.v1.default"

	pxfRequiredFQN FullName = "pxf.required"
	pxfDefaultFQN  FullName = "pxf.default"

	requiredDefaultNote = "the default says the field is never absent, " +
		"and required says that absence is an error"
)

// annotationSite is a set of the kinds of declaration an annotation can be
// written on.
type annotationSite uint16

const (
	siteField annotationSite = 1 << iota
	siteMessage
	siteOneof
	siteEnum
	siteEnumValue
	siteService
	siteMethod
	siteType
	siteFunction
	siteAnnotation

	siteAny = siteAnnotation<<1 - 1
)

// siteNames spell each site in a diagnostic, in the order a note lists them.
var siteNames = []struct {
	site annotationSite
	name string
}{
	{siteField, "a field"},
	{siteMessage, "a message"},
	{siteOneof, "a oneof"},
	{siteEnum, "an enum"},
	{siteEnumValue, "an enum value"},
	{siteService, "a service"},
	{siteMethod, "a method"},
	{siteType, "a type"},
	{siteFunction, "a function"},
	{siteAnnotation, "an annotation"},
}

// annotationPlacements is the RFC-001 §5.2 placement table. An annotation
// that is not listed, including every user annotation, is unrestricted.
var annotationPlacements = map[FullName]annotationSite{
	"protowire.schema.v1.validate":    siteField | siteMessage | siteType,
	canonicalRequiredFQN:              siteField | siteOneof | siteType,
	canonicalDefaultFQN:               siteField | siteType,
	"protowire.schema.v1.description": siteAny,
	"protowire.schema.v1.example":     siteField | siteMessage | siteType,
	"protowire.schema.v1.error_code":  siteField | siteMessage | siteType,
	"protowire.schema.v1.deprecated":  siteAny,
	canonicalSensitiveFQN:             siteField | siteMessage | siteType,
	internal.CanonicalHTTPFQN:         siteMethod,
}

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

// validateAnnotationPlacement enforces the RFC-001 §5.2 placement table and
// the rules on `@required` and `@default` that depend on more than the kind
// of declaration. Each of these otherwise compiles and lowers, and no engine
// can act on it.
//
// It runs after [propagateTypeAliasAnnotations], so a rule that arrives
// through a type alias is checked on the field it expanded onto. Checks key
// on the resolved FQN, so a user annotation named `default` is unaffected.
func validateAnnotationPlacement(file *File, r *report.Report) {
	for ty := range seq.Values(file.AllTypes()) {
		if ty.IsMapEntry() {
			continue // See allAnnotationUses.
		}
		site := siteMessage
		if ty.IsEnum() {
			site = siteEnum
		}
		checkSite(r, ty.Annotations(), site)
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
		checkSite(r, svc.Annotations(), siteService)
		for m := range seq.Values(svc.Methods()) {
			checkSite(r, m.Annotations(), siteMethod)
		}
	}
	for alias := range seq.Values(file.TypeAliases()) {
		checkSite(r, alias.Annotations(), siteType)
	}
	for fn := range seq.Values(file.Functions()) {
		checkSite(r, fn.Annotations(), siteFunction)
	}
	for ann := range seq.Values(file.Annotations()) {
		checkSite(r, ann.Annotations(), siteAnnotation)
	}
}

// checkSite reports every use in uses whose annotation the table does not
// allow on a declaration of the given kind.
func checkSite(r *report.Report, uses seq.Indexer[AnnotationUse], site annotationSite) {
	for u := range seq.Values(uses) {
		fqn := u.Target().FullName()
		allowed, ok := annotationPlacements[fqn]
		if !ok || allowed&site != 0 {
			continue
		}
		var valid []string
		var here string
		for _, s := range siteNames {
			if allowed&s.site != 0 {
				valid = append(valid, s.name)
			}
			if site == s.site {
				here = s.name
			}
		}
		r.Errorf("`@%s` is not valid on %s", u.Target().Name(), here).Apply(
			report.Snippet(u.AST()),
			report.Notef("`@%s` is valid on: %s", u.Target().Name(), strings.Join(valid, ", ")),
		)
	}
}

// validateMemberPlacement checks one field or enum value.
func validateMemberPlacement(r *report.Report, m Member) {
	if m.IsEnumValue() {
		checkSite(r, m.Annotations(), siteEnumValue)
		return
	}
	checkSite(r, m.Annotations(), siteField)

	var required, deflt AnnotationUse
	for u := range seq.Values(m.Annotations()) {
		switch u.Target().FullName() {
		case canonicalRequiredFQN:
			required = u
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
			deflt = u
			if what := noLiteralForm(m); what != "" {
				r.Errorf("`@default` is not valid on %s", what).Apply(
					report.Snippet(u.AST()),
					report.Snippetf(m.AST().Name(), "on this field"),
					report.Notef("`@default` carries one literal, and no literal denotes this field"),
				)
			}
		}
	}
	if !required.IsZero() && !deflt.IsZero() {
		r.Errorf("`@default` and `@required` on one field").Apply(
			report.Snippet(deflt.AST()),
			report.Snippetf(required.AST(), "required here"),
			report.Snippetf(m.AST().Name(), "on this field"),
			report.Notef(requiredDefaultNote),
		)
	}
}

// validateBracketRequiredDefault rejects a field that is both required and
// defaulted when a bracket option is one of the two. It runs after
// [resolveOptions], which [validateAnnotationPlacement] runs before, and that
// pass has already reported the pair written purely as annotations.
func validateBracketRequiredDefault(file *File, r *report.Report) {
	for ty := range seq.Values(file.AllTypes()) {
		if ty.IsMapEntry() || ty.IsEnum() {
			continue
		}
		for m := range seq.Values(ty.Members()) {
			checkBracketRequiredDefault(r, m)
		}
	}
	for ext := range seq.Values(file.AllExtensions()) {
		checkBracketRequiredDefault(r, ext)
	}
}

func checkBracketRequiredDefault(r *report.Report, m Member) {
	var required, deflt Value
	for v := range m.Options().Fields() {
		switch v.Field().FullName() {
		case pxfRequiredFQN:
			if set, _ := v.AsBool(); set {
				required = v
			}
		case pxfDefaultFQN:
			deflt = v
		}
	}
	if required.IsZero() && deflt.IsZero() {
		return
	}

	var requiredUse, defaultUse AnnotationUse
	for u := range seq.Values(m.Annotations()) {
		switch u.Target().FullName() {
		case canonicalRequiredFQN:
			requiredUse = u
		case canonicalDefaultFQN:
			defaultUse = u
		}
	}
	if (required.IsZero() && requiredUse.IsZero()) || (deflt.IsZero() && defaultUse.IsZero()) {
		return
	}

	r.Errorf("`required` and `default` on one field").Apply(
		report.Snippet(m.AST().Name()),
		report.Notef(requiredDefaultNote),
		report.Notef("this holds for `(pxf.required)` and `(pxf.default)` as well as "+
			"`@required` and `@default`, in any combination"),
	)
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
	checkSite(r, o.Annotations(), siteOneof)

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
