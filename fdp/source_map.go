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
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	pwsv1 "github.com/trendvidia/protocompile/internal/gen/protowire/schema/v1"
	"github.com/trendvidia/protocompile/ir"
	"github.com/trendvidia/protocompile/seq"
	"github.com/trendvidia/protocompile/source"
)

// emitSourceMap populates the [pwsv1.SourceMap] extension on
// FileOptions (50404) with [pwsv1.SourceEntry] entries covering
// every annotation lowering in `file`.
//
// `descriptor_path` follows the canonical RFC-001 §8.3.1 grammar,
// produced and parsed via [DescriptorPath] — the shared contract
// with protolsp / pxfed.
//
// Entry kinds, per the carrier proto's normative comments:
//
//   - Every annotation in a carrier's AnnotationList gets one entry
//     keyed `elementPath[annotationFQN#ordinal]`. The kind is
//     [pwsv1.EntryKind_FIELD_VALIDATE] on fields and extensions,
//     [pwsv1.EntryKind_MESSAGE_VALIDATE] on messages, and
//     [pwsv1.EntryKind_ANNOTATION_USE] on every other carrier.
//     Ordinals count same-named annotations on the carrier in list
//     order — including uses macro-expanded from type aliases, whose
//     provenance is separately captured by the TYPE_REFINEMENT entry.
//   - Each function-call site extracted from an expression argument
//     gets a [pwsv1.EntryKind_FUNCTION_CALL] entry keyed
//     `.../arg#i/call#j`, where i indexes the lowered
//     Annotation.args and j that argument's Expression.calls.
//   - A field or extension whose declared type resolved through a
//     type-alias chain gets one bare-elementPath
//     [pwsv1.EntryKind_TYPE_REFINEMENT] entry with the full chain.
//
// Returns true when the extension was attached.
func emitSourceMap(file *ir.File, target *descriptorpb.FileOptions) bool {
	out := &pwsv1.SourceMap{File: file.Path()}

	add := func(path ir.FullName, kind pwsv1.EntryKind, uses seq.Indexer[ir.AnnotationUse]) {
		addAnnotationEntries(&out.Entries, string(path), kind, uses)
	}

	for ty := range seq.Values(file.AllTypes()) {
		memberKind := pwsv1.EntryKind_FIELD_VALIDATE
		tyKind := pwsv1.EntryKind_MESSAGE_VALIDATE
		if ty.IsEnum() {
			// Enum and enum-value carriers are neither field- nor
			// message-validation surfaces.
			memberKind = pwsv1.EntryKind_ANNOTATION_USE
			tyKind = pwsv1.EntryKind_ANNOTATION_USE
		}
		add(ty.FullName(), tyKind, ty.Annotations())
		for field := range seq.Values(ty.Members()) {
			add(field.FullName(), memberKind, field.Annotations())
			if entry := buildTypeRefinementEntry(field); entry != nil {
				out.Entries = append(out.Entries, entry)
			}
		}
		for o := range seq.Values(ty.Oneofs()) {
			add(o.FullName(), pwsv1.EntryKind_ANNOTATION_USE, o.Annotations())
		}
	}

	for ext := range seq.Values(file.AllExtensions()) {
		add(ext.FullName(), pwsv1.EntryKind_FIELD_VALIDATE, ext.Annotations())
		if entry := buildTypeRefinementEntry(ext); entry != nil {
			out.Entries = append(out.Entries, entry)
		}
	}

	for svc := range seq.Values(file.Services()) {
		add(svc.FullName(), pwsv1.EntryKind_ANNOTATION_USE, svc.Annotations())
		for m := range seq.Values(svc.Methods()) {
			add(m.FullName(), pwsv1.EntryKind_ANNOTATION_USE, m.Annotations())
		}
	}

	for ann := range seq.Values(file.Annotations()) {
		add(ann.FullName(), pwsv1.EntryKind_ANNOTATION_USE, ann.Annotations())
	}

	for fn := range seq.Values(file.Functions()) {
		add(fn.FullName(), pwsv1.EntryKind_ANNOTATION_USE, fn.Annotations())
	}

	for ta := range seq.Values(file.TypeAliases()) {
		add(ta.FullName(), pwsv1.EntryKind_ANNOTATION_USE, ta.Annotations())
	}

	if len(out.Entries) == 0 {
		return false
	}
	proto.SetExtension(target, pwsv1.E_SourceMap, out)
	return true
}

// addAnnotationEntries appends the source-map entries for one
// carrier's annotation list: an entry of `kind` per lowered
// annotation, plus a FUNCTION_CALL entry per call site extracted
// from its expression arguments.
//
// The anchor ordinals and arg/call indexes mirror the carrier
// emission in [buildAnnotation]/[buildArg] exactly: unresolved uses
// (dropped from the carrier) don't advance ordinals, and dropped
// arguments don't advance arg indexes.
func addAnnotationEntries(entries *[]*pwsv1.SourceEntry, elementPath string, kind pwsv1.EntryKind, uses seq.Indexer[ir.AnnotationUse]) {
	var ordinals map[string]int
	for use := range seq.Values(uses) {
		target := use.Target()
		if target.IsZero() {
			continue // Not in the carrier; already diagnosed.
		}
		name := string(target.FullName())
		if ordinals == nil {
			ordinals = make(map[string]int)
		}
		ordinal := ordinals[name]
		ordinals[name]++

		anchor := DescriptorPath{
			Element:    elementPath,
			Annotation: name,
			Ordinal:    ordinal,
		}

		if entry := buildAnnotationUseEntry(anchor, kind, use); entry != nil {
			*entries = append(*entries, entry)
		}

		argIndex := 0
		for _, b := range use.ArgBindings() {
			arg := buildArg(use, b)
			if arg == nil {
				continue // Dropped from the carrier.
			}
			if b.Param.IsExpression() {
				for callIndex, call := range use.ExtractCalls(b.Arg) {
					path := anchor
					path.HasCall = true
					path.ArgIndex = argIndex
					path.CallIndex = callIndex
					*entries = append(*entries, &pwsv1.SourceEntry{
						Kind:           pwsv1.EntryKind_FUNCTION_CALL,
						DescriptorPath: path.String(),
						SourceLocation: sourceLocation(call.Span, call.Span.StartLoc()),
					})
				}
			}
			argIndex++
		}
	}
}

// buildAnnotationUseEntry lowers one annotation use site into a
// [pwsv1.SourceEntry]. Returns nil when no usable source span is
// available (e.g. a synthesized use without an AST node) — the
// anchor ordinal still advances in that case, since the annotation
// is in the carrier.
func buildAnnotationUseEntry(anchor DescriptorPath, kind pwsv1.EntryKind, use ir.AnnotationUse) *pwsv1.SourceEntry {
	ast := use.AST()
	if ast.IsZero() {
		return nil
	}
	span := ast.Span()
	if span.IsZero() {
		return nil
	}
	loc := span.StartLoc()
	return &pwsv1.SourceEntry{
		Kind:           kind,
		DescriptorPath: anchor.String(),
		SourceLocation: sourceLocation(span, loc),
	}
}

// buildTypeRefinementEntry lowers the type-alias chain a field's
// declared type resolved through into a [pwsv1.EntryKind_TYPE_REFINEMENT]
// SourceEntry.
//
// Returns nil when the field's declared type was concrete (no alias
// chain) or no usable source spans are available. The
// `source_location` is the position where the user wrote the alias
// name at the field site (in the consuming file); each link's
// `declaration_location` is the position of that alias's
// declaration (in the alias's defining file — possibly cross-file
// for an imported alias).
func buildTypeRefinementEntry(field ir.Member) *pwsv1.SourceEntry {
	chain := field.TypeAliasChain()
	if chain.Len() == 0 {
		return nil
	}

	entry := &pwsv1.SourceEntry{
		Kind:           pwsv1.EntryKind_TYPE_REFINEMENT,
		DescriptorPath: string(field.FullName()),
	}
	if typeAST := field.TypeAST(); !typeAST.IsZero() {
		span := typeAST.Span()
		if !span.IsZero() {
			entry.SourceLocation = sourceLocation(span, span.StartLoc())
		}
	}
	for a := range seq.Values(chain) {
		link := &pwsv1.TypeChainLink{TypeFqn: string(a.FullName())}
		if decl := a.AST(); !decl.IsZero() {
			span := decl.Span()
			if !span.IsZero() {
				link.DeclarationLocation = sourceLocation(span, span.StartLoc())
			}
		}
		entry.TypeChain = append(entry.TypeChain, link)
	}
	return entry
}

// sourceLocation packs a [source.Span] and its start [source.Location]
// into a [pwsv1.SourceLocation]. The span's file path takes
// precedence over the [ir.File.Path] of the emitting file: when an
// alias-propagated use lives in another file, the entry points back
// to that file directly.
func sourceLocation(span source.Span, loc source.Location) *pwsv1.SourceLocation {
	return &pwsv1.SourceLocation{
		File:   span.File.Path(),
		Line:   int32(loc.Line),
		Column: int32(loc.Column),
	}
}
