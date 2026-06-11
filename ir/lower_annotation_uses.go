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
	"github.com/trendvidia/protocompile/ast"
	"github.com/trendvidia/protocompile/id"
	"github.com/trendvidia/protocompile/internal/taxa"
	"github.com/trendvidia/protocompile/report"
	"github.com/trendvidia/protocompile/seq"
)

// resolveAnnotationUses materialises an [AnnotationUse] for every
// `@name(args)` use site attached to a carrier declaration in the
// file and resolves its name against the symbol table.
//
// Resolution failures (unknown symbol, symbol that's not an
// `annotation` declaration) produce diagnostics and a zero
// [AnnotationUse.Target]; the use is still recorded on the carrier so
// later passes can iterate without dropping entries.
//
// This is the Phase B2 entry point. It runs after
// [mergeImportedSymbolTables] so the file's imported symbol table
// covers both local annotations and any pulled in via import.
// Argument-shape type-checking against the resolved annotation's
// parameter signature is deferred to Phase B3.
func resolveAnnotationUses(file *File, r *report.Report) {
	for ty := range seq.Values(file.AllTypes()) {
		resolveAnnotationUsesOn(
			file, r, ty.AST().Annotations(), ty.Scope(),
			func(ids []id.ID[AnnotationUse]) {
				ty.Raw().annotationUses = ids
			},
		)
		for field := range seq.Values(ty.Members()) {
			resolveAnnotationUsesOn(
				file, r, field.AST().Annotations(), field.Parent().FullName(),
				func(ids []id.ID[AnnotationUse]) {
					field.Raw().annotationUses = ids
				},
			)
		}
		for o := range seq.Values(ty.Oneofs()) {
			resolveAnnotationUsesOn(
				file, r, o.AST().Annotations(), o.Container().FullName(),
				func(ids []id.ID[AnnotationUse]) {
					o.Raw().annotationUses = ids
				},
			)
		}
	}

	for ext := range seq.Values(file.AllExtensions()) {
		resolveAnnotationUsesOn(
			file, r, ext.AST().Annotations(), ext.Scope(),
			func(ids []id.ID[AnnotationUse]) {
				ext.Raw().annotationUses = ids
			},
		)
	}

	for svc := range seq.Values(file.Services()) {
		resolveAnnotationUsesOn(
			file, r, svc.AST().Annotations(), file.Package(),
			func(ids []id.ID[AnnotationUse]) {
				svc.Raw().annotationUses = ids
			},
		)
		for m := range seq.Values(svc.Methods()) {
			resolveAnnotationUsesOn(
				file, r, m.AST().Annotations(), svc.FullName(),
				func(ids []id.ID[AnnotationUse]) {
					m.Raw().annotationUses = ids
				},
			)
		}
	}

	for ann := range seq.Values(file.Annotations()) {
		resolveAnnotationUsesOn(
			file, r, ann.AST().Annotations(), file.Package(),
			func(ids []id.ID[AnnotationUse]) {
				ann.Raw().annotationUses = ids
			},
		)
	}
}

// resolveAnnotationUsesOn handles one carrier: walks its
// annotation-use AST nodes, resolves each name, allocates an
// [AnnotationUse] in the file arena, and writes the resulting ID
// slice back through the `attach` callback.
func resolveAnnotationUsesOn[Seq interface {
	Len() int
	At(int) ast.DeclAnnotationUse
}](
	file *File,
	r *report.Report,
	uses Seq,
	scope FullName,
	attach func([]id.ID[AnnotationUse]),
) {
	if uses.Len() == 0 {
		return
	}

	ids := make([]id.ID[AnnotationUse], 0, uses.Len())
	for i := range uses.Len() {
		use := uses.At(i)
		ids = append(ids, lowerAnnotationUse(file, r, use, scope))
	}
	attach(ids)
}

// lowerAnnotationUse allocates a [rawAnnotationUse] for a single
// use site and resolves its name against the file's imported symbol
// table. Returns the new ID. The resolved target is the zero ref on
// resolution failure (after a diagnostic was emitted).
func lowerAnnotationUse(file *File, r *report.Report, use ast.DeclAnnotationUse, scope FullName) id.ID[AnnotationUse] {
	raw := rawAnnotationUse{def: use.ID()}

	name := use.Name()
	if !name.IsZero() {
		// Path.Canonicalized() drops the optional leading '.' so absolute
		// vs relative paths produce identical interned names; the existing
		// symbolRef machinery already handles the absolute vs relative
		// distinction internally via Name.Absolute().
		sym := symbolRef{
			File:   file,
			Report: r,
			scope:  scope,
			name:   FullName(name.Canonicalized()),
			span:   name,
			accept: func(k SymbolKind) bool { return k == SymbolKindAnnotation },
			want:   taxa.Annotation,
		}.resolve()

		if !sym.IsZero() && sym.Kind() == SymbolKindAnnotation {
			raw.target = Ref[Symbol]{
				file: refFileFor(file, sym.Context()),
				id:   sym.ID(),
			}
			if sym.Context() != file {
				file.imports.MarkUsed(sym.Context())
			}
		}
	}

	return id.ID[AnnotationUse](file.arenas.annotationUses.NewCompressed(raw))
}

// refFileFor returns the [Ref.file] index for the file a resolved
// symbol came from. Local symbols get index 0; imported symbols are
// resolved against the file's import table.
func refFileFor(local *File, owner *File) int32 {
	if local == owner {
		return 0
	}
	idx, ok := local.imports.byPath[owner.InternedPath()]
	if !ok {
		return -1
	}
	return int32(idx) + 1
}
