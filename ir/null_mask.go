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
	"github.com/trendvidia/protocompile/internal/tags"
	"github.com/trendvidia/protocompile/ir/presence"
	"github.com/trendvidia/protocompile/report"
	"github.com/trendvidia/protocompile/seq"
	"github.com/trendvidia/protocompile/source"
)

// The `_null` mask (protocompile#248). PXF decoders record the paths of
// fields a document sets to null in a message's `_null` FieldMask, and
// encoders write those fields back as `field = null`; without the mask a
// null and an absent field are the same on the wire. With masks enabled,
// lowering adds the field to every message that has a field able to hold
// null, so a schema author does not have to declare it.
const (
	// NullMaskName is the name of the generated field.
	NullMaskName = "_null"
	// NullMaskJSONName is its JSON name, as protoc derives it from
	// [NullMaskName].
	NullMaskJSONName = "Null"
	// NullMaskNumber is the field number protowire claims for the mask in
	// every message that gets one: the highest legal field number, which
	// hand-numbered fields almost never reach. It is fixed so that no
	// schema change can move the mask.
	NullMaskNumber int32 = 536870911
	// NullMaskTypeName is the mask's message type.
	NullMaskTypeName FullName = "google.protobuf.FieldMask"
	// NullMaskImport is the file that declares [NullMaskTypeName].
	NullMaskImport = "google/protobuf/field_mask.proto"
)

// NullMasker selects files for [Session.NullMask].
//
// This is an interface, rather than a function, so that implementations
// can be comparable: a [Session] is part of incremental query keys.
type NullMasker interface {
	// NullMask reports whether messages in the file at path get a
	// generated `_null` mask.
	NullMask(path string) bool
}

// NullMasks reports whether messages in f get a generated `_null` mask:
// its session selects it, it is not one of protobuf's own files, and it
// does not import [NullMaskImport] with `import option`. Protobuf's own
// files never get one: rewriting google/protobuf/descriptor.proto, whose
// proto2 option messages all have fields with presence, would change
// protobuf's descriptors. A descriptor may not list one file as both a
// regular and an option dependency, so an option import turns masks off
// for the file, and lowering reports it.
func (f *File) NullMasks() bool {
	if !f.nullMaskSelected() {
		return false
	}
	_, optionImport := nullMaskOptionImport(f)
	return !optionImport
}

// NullMaskImport returns the file declaring [NullMaskTypeName] when f gets
// `_null` masks and does not import it itself, and nil otherwise.
// Descriptor generation lists it as a dependency once a message gets a
// mask, and [TopoSort] orders it before f, so a descriptor set carries
// it. It is not one of f's [File.Imports]: f cannot name its symbols.
func (f *File) NullMaskImport() *File {
	if f == nil {
		return nil
	}
	return f.nullMaskImport
}

func (f *File) nullMaskSelected() bool {
	return f != nil && f.session.NullMaskSelects(f.Path())
}

// NullMaskSelects reports whether [Session.NullMask] selects the file at
// path: it is set, it selects path, and path is not one of protobuf's own
// files. Those never get a mask, and must not ask for [NullMaskImport]
// either: field_mask.proto imports descriptor.proto implicitly, so a
// descriptor.proto that asked for field_mask.proto would close a cycle.
func (s *Session) NullMaskSelects(path string) bool {
	return s != nil && s.NullMask != nil && nullMaskPath(path) && s.NullMask.NullMask(path)
}

// importNullMask resolves [NullMaskImport] for a file that gets masks and
// does not import it directly. It runs once the file's own imports are
// in the table, and before name resolution.
func importNullMask(file *File, r *report.Report, importer Importer) {
	if !file.NullMasks() {
		return
	}
	for imp := range seq.Values(file.Imports()) {
		if imp.File != nil && imp.Path() == NullMaskImport {
			return
		}
	}
	mask, err := importer(-2, NullMaskImport, ast.DeclImport{})
	if err != nil || mask == nil {
		r.Errorf("could not import %q for the generated `%s` mask: %v", NullMaskImport, NullMaskName, err).Apply(
			report.Helpf("masks need the well-known %q to be available to the compiler", NullMaskImport),
		)
		return
	}
	file.nullMaskImport = mask
}

func nullMaskPath(path string) bool {
	return !strings.HasPrefix(path, "google/protobuf/")
}

// nullMaskOptionImport returns file's `import option` of
// [NullMaskImport], if it has one.
func nullMaskOptionImport(file *File) (Import, bool) {
	for imp := range seq.Values(file.Imports()) {
		if imp.Option && imp.File != nil && imp.Path() == NullMaskImport {
			return imp, true
		}
	}
	return Import{}, false
}

// NeedsNullMask reports whether lowering with masks enabled adds a
// `_null` field to ty. It does not consult [File.NullMasks]; callers
// check the file first.
func NeedsNullMask(ty Type) bool {
	return planNullMask(ty).add
}

// nullMaskPlan is what masks-enabled lowering does with one message: add
// the field, or not, and why not.
type nullMaskPlan struct {
	declared  Member        // a hand-declared _null, which wins
	number    Member        // an authored field already numbered NullMaskNumber
	json      Member        // an authored field whose JSON name is NullMaskJSONName
	extension ReservedRange // an extension range covering NullMaskNumber
	reserved  ReservedRange // a reserved range covering NullMaskNumber
	name      ReservedName  // `reserved "_null";`
	add       bool
}

func planNullMask(ty Type) nullMaskPlan {
	var plan nullMaskPlan
	// A map entry may hold only its key and value, and a message set only
	// extensions.
	if !ty.IsMessage() || ty.IsMapEntry() || ty.IsMessageSet() {
		return plan
	}
	nullable := false
	for m := range seq.Values(ty.Members()) {
		switch {
		case m.Name() == NullMaskName:
			plan.declared = m
		case m.Number() == NullMaskNumber:
			plan.number = m
		case m.JSONName() == NullMaskJSONName:
			plan.json = m
		}
		nullable = nullable || canHoldNull(m)
	}
	if !nullable || !plan.declared.IsZero() {
		return plan
	}
	for r := range seq.Values(ty.ExtensionRanges()) {
		if covers(r) {
			plan.extension = r
		}
	}
	for r := range seq.Values(ty.ReservedRanges()) {
		if covers(r) {
			plan.reserved = r
		}
	}
	for n := range seq.Values(ty.ReservedNames()) {
		if n.Name() == NullMaskName {
			plan.name = n
		}
	}
	plan.add = plan.number.IsZero() && plan.json.IsZero() &&
		plan.extension.IsZero() && plan.reserved.IsZero() && plan.name.IsZero()
	return plan
}

// canHoldNull reports whether a PXF null may bind to m: a singular field
// with explicit presence, as protowire#339 settles it. A message field,
// wrappers included, a proto3 `optional` scalar, a oneof member, a
// proto2 singular field and an editions field without IMPLICIT presence
// qualify; a proto3 scalar without `optional`, an IMPLICIT editions
// scalar, a repeated field and a map do not.
func canHoldNull(m Member) bool {
	switch m.Presence() {
	case presence.Shared, presence.Required:
		return true
	case presence.Repeated, presence.Unknown:
		return false
	}
	// Every singular message field has presence, whatever the IR
	// records for a proto3 one.
	if m.Element().IsMessage() {
		return true
	}
	if m.Context().Syntax().IsEdition() {
		// The IR records every unlabeled editions field as Explicit; its
		// presence is the resolved features.field_presence.
		v, _ := m.FeatureSet().Lookup(m.Context().builtins().FeaturePresence).Value().AsInt()
		return v != tags.FeatureSet_FieldPresence_Implicit
	}
	return m.Presence() == presence.Explicit
}

func covers(r ReservedRange) bool {
	start, end := r.Range()
	return start <= NullMaskNumber && NullMaskNumber <= end
}

// checkNullMasks reports, for every message in file, why masks-enabled
// lowering cannot add its `_null` field: an authored field that takes the
// number or the JSON name is an error, a range or name the author
// reserved is a warning (the message gets no mask, and its nulls do not
// survive protobuf binary), and a hand-declared `_null` that is not a
// singular FieldMask is an error. A well-formed hand-declared `_null`
// wins, and needs no report. So is an `import option` of
// [NullMaskImport] in a file where a message would get a mask.
func checkNullMasks(r *report.Report, file *File) {
	optionImport, hasOptionImport := nullMaskOptionImport(file)
	for ty := range seq.Values(file.AllTypes()) {
		plan := planNullMask(ty)
		switch {
		case plan.add && hasOptionImport:
			r.Errorf("`%s` is imported with `import option`, but the generated `%s` mask in `%s` needs it as a regular import",
				NullMaskImport, NullMaskName, ty.FullName()).Apply(
				report.Snippet(optionImport.Decl),
				report.Helpf("the mask is a `%s` field, which only a regular `import` can provide; "+
					"change this to `import \"%s\";`", NullMaskTypeName, NullMaskImport),
			)
			hasOptionImport = false // Once per file.
		case !plan.declared.IsZero():
			d := plan.declared
			if d.Presence() == presence.Repeated || d.Element().FullName() != NullMaskTypeName {
				r.Errorf("`%s` in `%s` must be a singular `%s`", NullMaskName, ty.FullName(), NullMaskTypeName).Apply(
					report.Snippet(d.AST()),
					report.Helpf("PXF records the fields a document sets to null in a field named `%s`; "+
						"rename this field, or declare it as `%s %s`", NullMaskName, NullMaskTypeName, NullMaskName),
				)
			}
		case !plan.number.IsZero():
			r.Errorf("field `%s` uses number %d, which protowire claims for the `%s` mask",
				plan.number.Name(), NullMaskNumber, NullMaskName).Apply(
				report.Snippet(plan.number.AST().Value()),
				report.Helpf("`%s` has a field that may hold null, so it gets a generated `%s %s = %d`; "+
					"renumber `%s`, or declare `%s` yourself at a free number",
					ty.FullName(), NullMaskTypeName, NullMaskName, NullMaskNumber, plan.number.Name(), NullMaskName),
			)
		case !plan.json.IsZero():
			r.Errorf("field `%s` has the JSON name `%s`, which the generated `%s` mask takes",
				plan.json.Name(), NullMaskJSONName, NullMaskName).Apply(
				report.Snippet(plan.json.AST()),
				report.Helpf("rename `%s`, or declare `%s` yourself", plan.json.Name(), NullMaskName),
			)
		case !plan.extension.IsZero():
			warnNoNullMask(r, ty, plan.extension.AST(), "an extension range covers number %d", NullMaskNumber)
		case !plan.reserved.IsZero():
			warnNoNullMask(r, ty, plan.reserved.AST(), "a reserved range covers number %d", NullMaskNumber)
		case !plan.name.IsZero():
			warnNoNullMask(r, ty, plan.name.AST(), "the name `%s` is reserved", NullMaskName)
		}
	}
}

func warnNoNullMask(r *report.Report, ty Type, at source.Spanner, why string, args ...any) {
	r.Warnf("`%s` gets no `%s` mask: "+why, append([]any{ty.FullName(), NullMaskName}, args...)...).Apply(
		report.Snippet(at),
		report.Helpf("a null in `%s` will not survive protobuf binary; to keep it, declare `%s %s` at a free number",
			ty.FullName(), NullMaskTypeName, NullMaskName),
	)
}
