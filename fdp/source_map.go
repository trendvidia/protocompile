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
// FileOptions (50404) with one [pwsv1.SourceEntry] per resolved
// annotation use site in `file`.
//
// `descriptor_path` syntax. The carrier proto comment marks the
// path syntax as "implementation-defined and matched by protolsp /
// pxfed." This implementation uses the carrier's fully-qualified
// name (dotted, e.g. "test.User.email" for a field), which is
// unambiguous within a file and the same identifier downstream
// tools already consume via [ir.Symbol.FullName]. Future revisions
// may switch to an array path matching
// [google.protobuf.SourceCodeInfo.Location.path]; the change
// would be a coordinated bump on protolsp / pxfed.
//
// MVP scope. Only [pwsv1.EntryKind_ANNOTATION_USE] entries are
// emitted at this revision. The other kinds —
// [pwsv1.EntryKind_TYPE_REFINEMENT],
// [pwsv1.EntryKind_FIELD_VALIDATE],
// [pwsv1.EntryKind_MESSAGE_VALIDATE], and
// [pwsv1.EntryKind_FUNCTION_CALL] — and the
// [pwsv1.TypeChainLink] type chain population are deferred to
// follow-up work as the underlying IR machinery for type
// refinements and validation lowering matures.
//
// Returns true when the extension was attached.
func emitSourceMap(file *ir.File, target *descriptorpb.FileOptions) bool {
	out := &pwsv1.SourceMap{File: file.Path()}

	add := func(path ir.FullName, use ir.AnnotationUse) {
		if use.TargetRef().IsZero() {
			return
		}
		entry := buildAnnotationUseEntry(string(path), use)
		if entry == nil {
			return
		}
		out.Entries = append(out.Entries, entry)
	}

	for ty := range seq.Values(file.AllTypes()) {
		for u := range seq.Values(ty.Annotations()) {
			add(ty.FullName(), u)
		}
		for field := range seq.Values(ty.Members()) {
			for u := range seq.Values(field.Annotations()) {
				add(field.FullName(), u)
			}
			if entry := buildTypeRefinementEntry(field); entry != nil {
				out.Entries = append(out.Entries, entry)
			}
		}
		for o := range seq.Values(ty.Oneofs()) {
			for u := range seq.Values(o.Annotations()) {
				add(o.FullName(), u)
			}
		}
	}

	for ext := range seq.Values(file.AllExtensions()) {
		for u := range seq.Values(ext.Annotations()) {
			add(ext.FullName(), u)
		}
		if entry := buildTypeRefinementEntry(ext); entry != nil {
			out.Entries = append(out.Entries, entry)
		}
	}

	for svc := range seq.Values(file.Services()) {
		for u := range seq.Values(svc.Annotations()) {
			add(svc.FullName(), u)
		}
		for m := range seq.Values(svc.Methods()) {
			for u := range seq.Values(m.Annotations()) {
				add(m.FullName(), u)
			}
		}
	}

	for ann := range seq.Values(file.Annotations()) {
		for u := range seq.Values(ann.Annotations()) {
			add(ann.FullName(), u)
		}
	}

	for fn := range seq.Values(file.Functions()) {
		for u := range seq.Values(fn.Annotations()) {
			add(fn.FullName(), u)
		}
	}

	for ta := range seq.Values(file.TypeAliases()) {
		for u := range seq.Values(ta.Annotations()) {
			add(ta.FullName(), u)
		}
	}

	if len(out.Entries) == 0 {
		return false
	}
	proto.SetExtension(target, pwsv1.E_SourceMap, out)
	return true
}

// buildAnnotationUseEntry lowers one annotation use site into a
// [pwsv1.SourceEntry]. Returns nil when no usable source span is
// available (e.g. a synthesized use without an AST node).
func buildAnnotationUseEntry(carrierFQN string, use ir.AnnotationUse) *pwsv1.SourceEntry {
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
		Kind:           pwsv1.EntryKind_ANNOTATION_USE,
		DescriptorPath: carrierFQN,
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
