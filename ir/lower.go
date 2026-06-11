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
	"fmt"
	"io"
	"sync"

	"github.com/trendvidia/protocompile/ast"
	"github.com/trendvidia/protocompile/internal/intern"
	"github.com/trendvidia/protocompile/parser"
	"github.com/trendvidia/protocompile/report"
	"github.com/trendvidia/protocompile/source"
	wkt "github.com/trendvidia/protocompile/wellknownimports/fs"
)

// Session is shared global configuration and state for all IR values that are
// being used together.
//
// It is used to track shared book-keeping.
//
// A zero [Session] is ready to use.
type Session struct {
	intern   intern.Table
	builtins builtinIDs

	optionalBuiltins map[intern.ID]struct{}

	// dpFallback is a session-scoped, lazily-built [*File] for the baked-in
	// well-known `google/protobuf/descriptor.proto`. It serves as the source
	// of builtin symbols when a user supplies a partial vendored
	// descriptor.proto override (e.g., one that only declares the *Options
	// messages with extra HACK fields). [resolveBuiltins] copies missing
	// symbols out of this file into the user's file's arenas.
	//
	// See the lengthy comment in lower_options.go on why this project
	// honours vendored descriptor.protos for option resolution.
	dpFallback *File

	once           sync.Once
	dpFallbackOnce sync.Once
}

// RecordInternStats enables instrumentation of the session's intern table.
//
// See [Session.InternStats].
func (s *Session) RecordInternStats() {
	s.intern.RecordStats(true)
}

// InternStats returns interning statistics, assuming [Session.RecordInternStats]
// was called first.
//
// This function is intended for performance monitoring only.
func (s *Session) InternStats() intern.Stats {
	return s.intern.Stats()
}

// Lower lowers an AST into an IR module.
//
// The ir package does not provide a mechanism for resolving imports; instead,
// they must be provided as an argument to this function.
func (s *Session) Lower(source *ast.File, errs *report.Report, importer Importer) (file *File, ok bool) {
	s.init()

	prior := len(errs.Diagnostics)
	file = &File{session: s, ast: source}
	file.path = file.session.intern.Intern(CanonicalizeFilePath(source.Path()))

	errs.SaveOptions(func() {
		errs.SuppressWarnings = errs.SuppressWarnings || file.IsDescriptorProto()
		lower(file, errs, importer)
	})

	ok = true
	for _, d := range errs.Diagnostics[prior:] {
		if d.Level() >= report.Error {
			ok = false
			break
		}
	}

	return file, ok
}

func (s *Session) init() {
	s.once.Do(func() {
		s.intern.Preload(&s.builtins)
		s.optionalBuiltins = optionalBuiltinIDs(&s.builtins)
	})
}

// fallbackDescriptorProto lowers the baked-in well-known
// `google/protobuf/descriptor.proto` once per session and returns the
// resulting [*File]. It is the source of builtin symbols when the
// user-supplied descriptor.proto is a partial vendored override that
// only declares a subset of the descriptor types.
//
// Lowering the baked-in WKT goes through the same [Session.Lower] flow
// as any other file; descriptor.proto has no imports of its own, so the
// no-op importer never fires. Diagnostics produced during this internal
// lowering are discarded because they can only reflect a bug in the
// baked-in WKT itself, not in the user's input.
func (s *Session) fallbackDescriptorProto() *File {
	s.dpFallbackOnce.Do(func() {
		f, err := wkt.FS().Open(DescriptorProtoPath)
		if err != nil {
			panic(fmt.Errorf("protocompile/ir: open baked-in %q: %w", DescriptorProtoPath, err))
		}
		text, err := io.ReadAll(f)
		_ = f.Close()
		if err != nil {
			panic(fmt.Errorf("protocompile/ir: read baked-in %q: %w", DescriptorProtoPath, err))
		}

		// Parse with the canonical path so [File.IsDescriptorProto]
		// returns true and [resolveBuiltins] populates dpBuiltins on the
		// fallback. The descriptor.proto WKT has no imports, so the
		// no-op importer is never called.
		src := source.NewFile(DescriptorProtoPath, string(text))
		var rpt report.Report
		astFile, ok := parser.Parse(DescriptorProtoPath, src, &rpt)
		if !ok {
			panic(fmt.Errorf("protocompile/ir: parse baked-in %q: %v",
				DescriptorProtoPath, rpt.Diagnostics))
		}

		var noopImporter Importer = func(int, string, ast.DeclImport) (*File, error) {
			panic(fmt.Errorf("protocompile/ir: baked-in %q unexpectedly requested an import",
				DescriptorProtoPath))
		}
		s.dpFallback, _ = s.Lower(astFile, &rpt, noopImporter)
	})
	return s.dpFallback
}

func lower(file *File, r *report.Report, importer Importer) {
	defer r.CatchICE(false, func(d *report.Diagnostic) {
		d.Apply(report.Notef("while lowering %q", file.Path()))
	})

	// First, build the Type graph for this file.
	(&walker{File: file, Report: r}).walk()

	// Now, resolve all the imports.
	buildImports(file, r, importer)

	generateMapEntries(file, r)

	// Next, we can build various symbol tables in preparation for name
	// resolution.
	buildLocalSymbols(file)
	mergeImportedSymbolTables(file, r)

	// Perform "early" name resolution, i.e. field names and extension types.
	// Name resolution always proceeds regardless of builtin validity; field
	// types, method types, and extensions use the symbol table, not builtins.
	resolveNames(file, r)
	resolveEarlyOptions(file)

	// Resolve annotation use sites against the symbol table (Phase B2 of
	// the PSE annotation work). This must run after the symbol table is
	// built and merged, but doesn't depend on options or features.
	resolveAnnotationUses(file, r)

	// Phase B3: classify annotation parameter types (scalar /
	// `expression` / `any` / user type), then type-check each use
	// site's argument list against the resolved signature.
	resolveAnnotationParamTypes(file, r)
	validateAnnotationParamDefaults(file, r)
	validateAnnotationUseArgs(file, r)

	// Perform constant evaluation.
	evaluateFieldNumbers(file, r)

	// Check for number overlaps now that we have numbers loaded.
	buildFieldNumberRanges(file, r)

	// Perform "late" name resolution, that is, options.
	resolveOptions(file, r)

	// Figure out what the option targets of everything is, and validate that
	// those are respected. This requires options to be resolved, and must be
	// done in two separate steps.
	populateOptionTargets(file, r)
	validateOptionTargets(file, r)

	// Build feature info for validating features after they are constructed.
	// Then validate all feature settings throughout the file.
	buildAllFeatureInfo(file, r)
	validateAllFeatures(file, r)

	populateJSONNames(file, r)

	// Validate all the little constraint details that didn't get caught above.
	diagnoseUnusedImports(file, r)
	validateConstraints(file, r)
	checkDeprecated(file, r)
}
