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

package printer_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trendvidia/protocompile/ast"
	"github.com/trendvidia/protocompile/ast/printer"
	"github.com/trendvidia/protocompile/internal/golden"
	"github.com/trendvidia/protocompile/parser"
	"github.com/trendvidia/protocompile/report"
	"github.com/trendvidia/protocompile/seq"
	"github.com/trendvidia/protocompile/source"
	"github.com/trendvidia/protocompile/token"
)

// TestRoundTrip exercises round-tripping a protobuf source through [printer.PrintFile].
func TestRoundTrip(t *testing.T) {
	t.Parallel()

	corpus := golden.Corpus{
		Root:       "testdata/roundtrip",
		Extensions: []string{"proto"},
	}

	corpus.Run(t, func(t *testing.T, path, text string, _ []string) {
		errs := &report.Report{}
		file, _ := parser.Parse(path, source.NewFile(path, text), errs)
		for _, d := range errs.Diagnostics {
			if d.Level() <= report.Warning {
				t.Logf("parse warning: %q", d.Message())
			}
		}

		got, err := printer.PrintFile(printer.Options{}, file)
		if err != nil {
			t.Fatalf("PrintFile: %v", err)
		}
		if msg := golden.CompareAndDiff(got, text); msg != "" {
			t.Errorf("round-trip mismatch:\n%s", msg)
		}
	})
}

// TestPrint exercises [printer.Print] on each declaration in the round-trip
// corpus. The concatenated output of [printer.Print] on each AST decl is
// expected to be equivalent to the output of [printer.PrintFile], minus any
// file-level trailing trivia, since those will not be captured by AST decls.
func TestPrint(t *testing.T) {
	t.Parallel()

	corpus := golden.Corpus{
		Root:       "testdata/roundtrip",
		Extensions: []string{"proto"},
	}

	corpus.Run(t, func(t *testing.T, path, text string, _ []string) {
		errs := &report.Report{}
		file, _ := parser.Parse(path, source.NewFile(path, text), errs)
		for _, d := range errs.Diagnostics {
			if d.Level() <= report.Warning {
				t.Logf("parse warning: %q", d.Message())
			}
		}

		var actual strings.Builder
		for decl := range seq.Values(file.Decls()) {
			actual.WriteString(printer.Print(printer.Options{}, decl))
		}

		whole, err := printer.PrintFile(printer.Options{}, file)
		if err != nil {
			t.Fatalf("PrintFile: %v", err)
		}
		// The concatenated per-decl output must be a prefix of the
		// whole-file output. [printer.PrintFile] may emit trailing
		// detached trivia (e.g. EOF comments) that are not associated
		// with any decl, so it can have content after this prefix.
		if !strings.HasPrefix(whole, actual.String()) {
			if msg := golden.CompareAndDiff(actual.String(), whole); msg != "" {
				t.Errorf("Print over decls is not a prefix of PrintFile:\n%s", msg)
			}
		}
	})
}

// TestFormat exercises the printer's format mode against goldens in
// testdata/format. Each <name>.proto is formatted under two presets
// and compared against the corresponding golden:
//
//   - <name>.proto.legacy.txt: [printer.Legacy] — reproduces the
//     legacy protobuf formatter's behavior byte-for-byte.
//   - <name>.proto.default.txt: [printer.Default] — the recommended
//     modern preset (dynamic layout, width-aware breaking at 100
//     cols, no comment-text rewriting).
//
// Each preset's output must re-parse cleanly and be idempotent under
// a second format pass.
//
// To regenerate goldens:
//
//	PROTOCOMPILE_REFRESH=** go test ./experimental/ast/printer/... -run TestFormat
func TestFormat(t *testing.T) {
	t.Parallel()

	corpus := golden.Corpus{
		Root:       "testdata/format",
		Extensions: []string{"proto"},
		Refresh:    "PROTOCOMPILE_REFRESH",
		Outputs: []golden.Output{
			{Extension: "legacy.txt"},
			{Extension: "default.txt"},
		},
	}

	presets := []struct {
		label string
		opts  printer.Options
	}{
		{"legacy", printer.Options{Format: true, Formatting: printer.Legacy()}},
		{"default", printer.Options{Format: true, Formatting: printer.Default()}},
	}

	corpus.Run(t, func(t *testing.T, path, text string, outputs []string) {
		errs := &report.Report{}
		file, _ := parser.Parse(path, source.NewFile(path, text), errs)
		hasParseErrors := false
		for _, d := range errs.Diagnostics {
			if d.Level() <= report.Error {
				hasParseErrors = true
			}
			if d.Level() <= report.Warning {
				t.Logf("parse warning: %q", d.Message())
			}
		}

		for i, preset := range presets {
			got, err := printer.PrintFile(preset.opts, file)
			if err != nil {
				t.Errorf("[%s] PrintFile: %v", preset.label, err)
				continue
			}
			outputs[i] = got

			if hasParseErrors {
				continue
			}

			// Verify the output is valid protobuf by re-parsing it.
			errs2 := &report.Report{}
			file2, _ := parser.Parse(path, source.NewFile(path, got), errs2)
			for _, d := range errs2.Diagnostics {
				if d.Level() <= report.Error {
					t.Errorf("[%s] formatted output does not re-parse: %v", preset.label, d)
				}
			}

			// Verify idempotency.
			got2, err := printer.PrintFile(preset.opts, file2)
			if err != nil {
				t.Errorf("[%s] PrintFile (idempotency): %v", preset.label, err)
				continue
			}
			if msg := golden.CompareAndDiff(got2, got); msg != "" {
				t.Errorf("[%s] formatting is not idempotent:\n%s", preset.label, msg)
			}
		}
	})
}

// TestAnnotationUses checks that printing keeps every annotation use,
// leading and trailing, in round-trip mode and under each format preset:
// re-parsing the output must yield the same uses, in the same order, with
// the same arguments up to whitespace.
func TestAnnotationUses(t *testing.T) {
	t.Parallel()

	const path = "testdata/roundtrip/annotation_uses.proto"
	text, err := os.ReadFile(path)
	require.NoError(t, err)

	parse := func(t *testing.T, text string) *ast.File {
		t.Helper()
		errs := &report.Report{}
		file, _ := parser.Parse(path, source.NewFile(path, text), errs)
		for _, d := range errs.Diagnostics {
			require.Greater(t, d.Level(), report.Error, "parse error: %s", d.Message())
		}
		return file
	}
	want := annotationUses(parse(t, string(text)))
	// A positive marker that the corpus exercises what it claims to,
	// so an empty extraction cannot pass vacuously.
	require.Len(t, want, 27)

	for _, opts := range []printer.Options{
		{},
		{Format: true, Formatting: printer.Legacy()},
		{Format: true, Formatting: printer.Default()},
	} {
		got, err := printer.PrintFile(opts, parse(t, string(text)))
		require.NoError(t, err)
		assert.Equal(t, want, annotationUses(parse(t, got)), "format=%v:\n%s", opts.Format, got)
	}
}

// annotationUses lists every annotation use in file, in source order, as
// its text with whitespace removed.
func annotationUses(file *ast.File) []string {
	var uses []string
	var walk func(decls seq.Indexer[ast.DeclAny])
	use := func(ann ast.DeclAnnotationUse) {
		uses = append(uses, strings.Join(strings.Fields(ann.Span().Text()), ""))
	}
	collect := func(anns seq.Indexer[ast.DeclAnnotationUse]) {
		for ann := range seq.Values(anns) {
			use(ann)
		}
	}
	walk = func(decls seq.Indexer[ast.DeclAny]) {
		for decl := range seq.Values(decls) {
			switch decl.Kind() {
			case ast.DeclKindDef:
				collect(decl.AsDef().Annotations())
				walk(decl.AsDef().Body().Decls())
			case ast.DeclKindType:
				collect(decl.AsType().Annotations())
			case ast.DeclKindFunction:
				collect(decl.AsFunction().Annotations())
			case ast.DeclKindAnnotation:
				collect(decl.AsAnnotation().Annotations())
			case ast.DeclKindAnnotationUse:
				use(decl.AsAnnotationUse())
			}
		}
	}
	walk(file.Decls())
	return uses
}

// TestFormatKeepsComments checks that formatting deletes no comment: for
// every corpus file that parses, each word of each comment in the source
// appears, as often, in the comments of the output under each preset.
// Legacy may rewrite `//` to `/* */` and re-flow block comments, so words
// are compared, not comment text (#273).
func TestFormatKeepsComments(t *testing.T) {
	t.Parallel()

	var paths []string
	for _, root := range []string{"testdata/format", "testdata/roundtrip", "testdata/bufformat"} {
		require.NoError(t, filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
			if err == nil && strings.HasSuffix(path, ".proto") {
				paths = append(paths, path)
			}
			return err
		}))
	}
	checked := 0
	for _, path := range paths {
		text, err := os.ReadFile(path)
		require.NoError(t, err)
		errs := &report.Report{}
		file, _ := parser.Parse(path, source.NewFile(path, string(text)), errs)
		if slices.ContainsFunc(errs.Diagnostics, func(d report.Diagnostic) bool { return d.Level() <= report.Error }) {
			continue
		}
		want := commentWords(file)
		for _, preset := range []struct {
			name string
			opts printer.Formatting
		}{{"legacy", printer.Legacy()}, {"default", printer.Default()}} {
			out, err := printer.PrintFile(printer.Options{Format: true, Formatting: preset.opts}, file)
			require.NoError(t, err)
			reparsed, _ := parser.Parse(path, source.NewFile(path, out), &report.Report{})
			got := commentWords(reparsed)
			for word, n := range want {
				assert.GreaterOrEqual(t, got[word], n, "%s [%s]: comment word %q lost", path, preset.name, word)
			}
		}
		checked++
	}
	// A positive marker that the corpus was found and parsed.
	require.Greater(t, checked, 100)
}

// commentWords counts the words in file's comments.
func commentWords(file *ast.File) map[string]int {
	words := map[string]int{}
	for tok := range file.Stream().All() {
		if tok.Kind() != token.Comment {
			continue
		}
		text := strings.NewReplacer("//", " ", "/*", " ", "*/", " ").Replace(tok.Text())
		for _, w := range strings.Fields(text) {
			if w = strings.Trim(w, "*"); w != "" {
				words[w]++
			}
		}
	}
	return words
}
