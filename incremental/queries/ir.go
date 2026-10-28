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
	"fmt"

	"github.com/trendvidia/protocompile/ast"
	"github.com/trendvidia/protocompile/incremental"
	"github.com/trendvidia/protocompile/internal/ext/iterx"
	"github.com/trendvidia/protocompile/ir"
	"github.com/trendvidia/protocompile/report"
	"github.com/trendvidia/protocompile/source"
)

// IR is an [incremental.Query] for the lowered IR of a Protobuf file.
//
// IR queries with different Openers are considered distinct.
type IR struct {
	source.Opener // Must be comparable.
	*ir.Session
	Path string

	// Used for tracking if this IR request was triggered by an import, for
	// constructing a cycle error. This is not part of the query's key.
	request ast.DeclImport
}

var _ incremental.Query[*ir.File] = IR{}

// Key implements [incremental.Query].
func (i IR) Key() any {
	type key struct {
		o    source.Opener
		s    *ir.Session
		path string
	}
	return key{i.Opener, i.Session, i.Path}
}

// Execute implements [incremental.Query].
func (i IR) Execute(t *incremental.Task) (*ir.File, error) {
	t.Report().Options.Stage += stageIR

	r, err := incremental.Resolve(t, AST{
		Opener: i.Opener,
		Path:   i.Path,
	})
	if err != nil {
		return nil, err
	}
	if r[0].Fatal != nil {
		return nil, r[0].Fatal
	}

	file := r[0].Value

	// Check for descriptor.proto in the opener. If it's not present, that's
	// going to produce a mess of weird errors, and we want this to be an ICE.
	dp, err := incremental.Resolve(t, File{
		Opener:      i.Opener,
		Path:        ir.DescriptorProtoPath,
		ReportError: false,
	})
	if err != nil {
		return nil, err
	}
	if dp[0].Fatal != nil {
		t.Report().Fatalf("could not import `%s`", ir.DescriptorProtoPath).Apply(
			report.Notef("protocompile is not configured correctly"),
			report.Helpf("`descriptor.proto` must always be available, since it is "+
				"required for correctly implementing Protobuf's semantics. "+
				"If you are using protocompile as a library, you may be missing a "+
				"source.WKTs() in your source.Opener setup."),
		)
		return nil, dp[0].Fatal
	}

	// A file the session gives `_null` masks also needs the mask's type,
	// whether or not it imports it: lowering asks for it with n == -2
	// (protocompile#248). Resolving it for a file that turns out to need
	// none, or imports it itself, costs one cached query. Lowering selects
	// by the canonical path, so this must too.
	masked := i.Session.NullMaskSelects(ir.CanonicalizeFilePath(i.Path))

	// Resolve all of the imports in the AST.
	direct := iterx.Count(file.Imports())
	descriptorQuery, maskQuery := direct, -1
	extra := 1 // The descriptor.proto query.
	if masked {
		maskQuery = direct + 1
		extra++
	}
	queries := make([]incremental.Query[*ir.File], direct+extra)
	errors := make([]error, len(queries))
	for j, decl := range iterx.Enumerate(file.Imports()) {
		lit := decl.ImportPath().AsLiteral().AsString()
		path := lit.Text()
		path = ir.CanonicalizeFilePath(path)

		if lit.IsZero() {
			// The import path is already legalized in [parser.legalizeImport()], if it is not
			// a valid path, we just set a [incremental.ZeroQuery] so that we don't get a nil
			// query for index j.
			queries[j] = incremental.ZeroQuery[*ir.File]{}
			continue
		}

		r, err := incremental.Resolve(t, File{
			Opener:      i.Opener,
			Path:        path,
			ReportError: false,
		})
		if err != nil {
			return nil, err
		}

		if err := r[0].Fatal; err != nil {
			queries[j] = incremental.ZeroQuery[*ir.File]{}
			errors[j] = r[0].Fatal
			continue
		}

		queries[j] = IR{
			Opener:  i.Opener,
			Session: i.Session,
			Path:    path,
			request: decl,
		}
	}

	queries[descriptorQuery] = IR{
		Opener:  i.Opener,
		Session: i.Session,
		Path:    ir.DescriptorProtoPath,
	}
	if masked {
		// As for an import: a missing file is not reported here, but by
		// lowering, and only if a message in the file needs the mask.
		r, err := incremental.Resolve(t, File{
			Opener:      i.Opener,
			Path:        ir.NullMaskImport,
			ReportError: false,
		})
		if err != nil {
			return nil, err
		}
		if err := r[0].Fatal; err != nil {
			queries[maskQuery] = incremental.ZeroQuery[*ir.File]{}
			errors[maskQuery] = err
		} else {
			queries[maskQuery] = IR{
				Opener:  i.Opener,
				Session: i.Session,
				Path:    ir.NullMaskImport,
			}
		}
	}

	imports, err := incremental.Resolve(t, queries...)
	if err != nil {
		return nil, err
	}

	importer := func(n int, _ string, _ ast.DeclImport) (*ir.File, error) {
		switch n {
		case -1:
			// The lowering code will call the importer with n == -1 if it needs
			// descriptor.proto but it isn't imported (transitively).
			n = descriptorQuery
		case -2:
			// ...and with n == -2 for the `_null` mask's type.
			if maskQuery < 0 {
				return nil, fmt.Errorf("%s was not resolved for %s", ir.NullMaskImport, i.Path)
			}
			n = maskQuery
		}

		result := imports[n]
		switch err := result.Fatal.(type) {
		case nil:
			return result.Value, errors[n]

		case *incremental.ErrCycle:
			// We need to walk the cycle and extract which imports are
			// responsible for the failure.
			cyc := new(ir.ErrCycle)
			for _, q := range err.Cycle {
				irq, ok := incremental.AsTyped[IR](q)
				if !ok {
					continue
				}
				if !irq.request.IsZero() {
					cyc.Cycle = append(cyc.Cycle, irq.request)
				}
			}

			return nil, cyc

		default:
			return nil, err
		}
	}

	ir, _ := i.Session.Lower(file, t.Report(), importer)
	return ir, nil
}
