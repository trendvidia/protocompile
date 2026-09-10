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

package queries

import (
	"slices"

	"github.com/trendvidia/protocompile/incremental"
	"github.com/trendvidia/protocompile/internal/ext/mapsx"
	"github.com/trendvidia/protocompile/internal/ext/slicesx"
	"github.com/trendvidia/protocompile/ir"
	"github.com/trendvidia/protocompile/report"
	"github.com/trendvidia/protocompile/seq"
	"github.com/trendvidia/protocompile/source"
)

// Link is an [incremental.Query] for the lowered IR files [*ir.File] of the given
// Protobuf source workspace [source.Workspace]. This query links the compilation of the
// given sources together and allows us to additional checks across the sources,
// e.g. duplicate symbols and extensions across the given [source.Workspace].
//
// Link queries with different [source.Opener]s and/or [source.Workspace]s are
// considered distinct.
type Link struct {
	source.Opener // Must be comparable.
	*ir.Session
	source.Workspace // Must be comparable.
}

var _ incremental.Query[[]*ir.File] = Link{}

// Key implements [incremental.Query].
func (l Link) Key() any {
	return l
}

// Execute implements [incremental.Query].
func (l Link) Execute(t *incremental.Task) ([]*ir.File, error) {
	t.Report().Options.Stage += stageLink

	queries := slicesx.Transform(
		l.Workspace.Paths(),
		func(path string) incremental.Query[*ir.File] {
			return IR{
				Opener:  l.Opener,
				Session: l.Session,
				Path:    path,
			}
		},
	)

	results, err := incremental.Resolve(t, queries...)
	if err != nil {
		return nil, err
	}

	files, err := results.Slice()
	if err != nil {
		return nil, err
	}

	LinkChecks(t.Report(), files...)
	return files, nil
}

// LinkChecks runs the whole-compilation checks that no single [IR] query
// can: duplicate exported symbols across files, and duplicate extension
// tags across files and their transitive imports. [Link] runs them over
// a [source.Workspace]; a caller that resolves [IR] queries itself, such
// as the compiler's Compile, runs them over the roots it got back.
//
// Symbols are already deduplicated within each file's import closure
// while its IR is lowered, so files is checked as given. Extension
// numbers are not deduplicated among imports during the IR queries, so
// every transitive import is added to that check, each file once.
func LinkChecks(r *report.Report, files ...*ir.File) {
	ir.DedupExportedSymbols(r, files...)

	seen := make(map[string]*ir.File, len(files))
	for _, file := range files {
		seen[file.Path()] = file
	}
	var requiredImports []*ir.File
	for _, file := range files {
		for imp := range seq.Values(file.Imports()) {
			file, inserted := mapsx.Add(seen, imp.Path(), imp.File)
			if inserted {
				requiredImports = append(requiredImports, file)
			}
		}
	}
	ir.DedupExtensions(r, slices.Concat(files, requiredImports)...)
}
