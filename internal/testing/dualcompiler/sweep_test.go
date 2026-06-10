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

package dualcompiler_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/trendvidia/protocompile"
	"github.com/trendvidia/protocompile/internal/testing/dualcompiler"
)

// pointerRegex matches Go-style pointer addresses ("0x" + hex) so the
// sweep report stays deterministic across runs. Error formatting from
// the experimental compiler stringifies internal structs that contain
// pointers; we strip those for golden-file stability.
var pointerRegex = regexp.MustCompile(`0x[0-9a-f]+`)

// absPathRegex matches absolute filesystem paths so the sweep report
// is portable across machines. Errors from the file resolver embed
// the full path of any missing dependency.
//
// The greedy `*` (not `*?`) anchors on the *last* `/protocompile/` in
// the path. GitHub Actions clones into
// `/home/runner/work/protocompile/protocompile/...` — a non-greedy
// match would stop at the first occurrence and leave a stray
// `protocompile/` in the report. (Originally fixed in PR #35; PR
// #34's squash merge silently reverted it because that branch was
// based on a version of this file from before #35 landed.)
var absPathRegex = regexp.MustCompile(`/[^ :]*/protocompile/`)

// mustMatch is the list of fixtures that the B-track has already
// brought to BOTH_OK_MATCH and that future PRs must not regress. Each
// entry is a path relative to the repo root.
//
// Adding a fixture here is the explicit signal that a divergence has
// been closed: TestSweep now fails if it does not classify as
// BOTH_OK_MATCH. The golden sweep.txt can still be regenerated freely
// for other categories, but a regression on any of these is a hard
// failure.
//
// Removing or commenting out an entry to silence a failure should not
// happen lightly: it permanently records a regression and gives up
// the equivalence work the original PR shipped. Any such change
// belongs in its own PR with reviewer sign-off.
//
// Closed by the B-track PRs:
//
//	#23 — TestSweep harness lit up the initial 9 matching fixtures.
//	#25 — float-default precision: desc_test_defaults.proto.
//	#26 — enum-alias preservation: desc_test_defaults.proto (final close).
//	#28 — typed extensions via per-file dynamicpb resolver: desc_test_complex.proto, desc_test_comments.proto, desc_test_options.proto.
//	#34 — field-number sorted message-literal wire output: options/options.proto.
var mustMatch = []string{
	"internal/testdata/desc_test1.proto",
	"internal/testdata/desc_test2.proto",
	"internal/testdata/desc_test_comments.proto",
	"internal/testdata/desc_test_complex.proto",
	"internal/testdata/desc_test_defaults.proto",
	"internal/testdata/desc_test_field_types.proto",
	"internal/testdata/desc_test_options.proto",
	"internal/testdata/desc_test_proto3.proto",
	"internal/testdata/desc_test_proto3_optional.proto",
	"internal/testdata/desc_test_wellknowntypes.proto",
	"internal/testdata/nopkg/desc_test_nopkg.proto",
	"internal/testdata/nopkg/desc_test_nopkg_new.proto",
	"internal/testdata/options/options.proto",
	"internal/testdata/pkg/desc_test_pkg.proto",
}

// TestSweep compiles every .proto fixture under the repo's main testdata
// roots through both the legacy and experimental pipelines, classifies
// each fixture's outcome, and writes a single text report.
//
// The report lives at testdata/sweep.txt under this package's directory.
// Refresh it with PROTOCOMPILE_REFRESH=1 go test ./internal/testing/dualcompiler -run TestSweep.
//
// In addition to the golden-file comparison, the test asserts that
// every fixture listed in [mustMatch] remains BOTH_OK_MATCH. That
// gates against silent regressions: a refresh updates the per-fixture
// lines but does not bypass the must-match check.
//
// The purpose is to make the divergence surface between the two
// pipelines visible at a glance so the B-track migration knows what
// gaps to close. The report is intentionally coarse: it only
// categorises each fixture (both ok and matching, both ok but
// differing, only one succeeded, or both failed). Detailed analysis
// follows up per category.
func TestSweep(t *testing.T) {
	t.Parallel()

	fixtures := collectFixtures(t)
	require.NotEmpty(t, fixtures, "sweep found no fixtures")

	results := make([]sweepResult, len(fixtures))
	for i, f := range fixtures {
		results[i] = classifyFixture(t, f)
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].Path < results[j].Path
	})

	assertMustMatch(t, results)

	report := renderReport(results)

	goldenPath := filepath.Join("testdata", "sweep.txt")
	if os.Getenv("PROTOCOMPILE_REFRESH") != "" {
		require.NoError(t, os.WriteFile(goldenPath, []byte(report), 0o644))
		t.Logf("refreshed %s", goldenPath)
		return
	}

	expected, err := os.ReadFile(goldenPath)
	require.NoError(t, err, "missing golden %s; refresh with PROTOCOMPILE_REFRESH=1", goldenPath)
	if got := string(expected); got != report {
		t.Errorf("sweep report drift; refresh with PROTOCOMPILE_REFRESH=1 if expected:\n%s",
			cmp.Diff(got, report))
	}
}

type fixture struct {
	// Path is the relative path from the repo root, used in the report
	// and as the identifier for diffs.
	Path string
	// ImportPaths is the list of resolver roots needed to import the
	// file's dependencies.
	ImportPaths []string
	// File is the path the resolver should use to find this fixture
	// (typically the basename within one of the import roots).
	File string
}

type sweepResult struct {
	Path     string
	Category string
	Notes    string
}

// collectFixtures walks the repo's main proto-fixture roots and returns
// one entry per .proto file. Editions and experimental fixtures are
// out of scope; their pipelines move at a different pace.
func collectFixtures(t *testing.T) []fixture {
	t.Helper()

	repoRoot := repoRoot(t)

	roots := []struct {
		dir         string
		importPaths []string
	}{
		{dir: "internal/testdata", importPaths: []string{"internal/testdata"}},
		{dir: "internal/testdata/options", importPaths: []string{"internal/testdata", "internal/testdata/options"}},
		{dir: "internal/testdata/nopkg", importPaths: []string{"internal/testdata", "internal/testdata/nopkg"}},
		{dir: "internal/testdata/pkg", importPaths: []string{"internal/testdata", "internal/testdata/pkg"}},
		{dir: "parser/testdata", importPaths: []string{"parser/testdata"}},
	}

	var out []fixture
	for _, r := range roots {
		entries, err := os.ReadDir(filepath.Join(repoRoot, r.dir))
		if err != nil {
			t.Fatalf("read %s: %v", r.dir, err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".proto") {
				continue
			}
			path := filepath.ToSlash(filepath.Join(r.dir, e.Name()))

			// The resolver locates files by their import-path-relative
			// name. The fixture's name is its basename relative to the
			// first import path that contains it.
			file := e.Name()
			for _, ip := range r.importPaths {
				candidate, err := filepath.Rel(ip, filepath.Join(r.dir, e.Name()))
				if err == nil && !strings.HasPrefix(candidate, "..") {
					file = filepath.ToSlash(candidate)
					break
				}
			}

			out = append(out, fixture{
				Path:        path,
				ImportPaths: r.importPaths,
				File:        file,
			})
		}
	}
	return out
}

// classifyFixture runs both pipelines on a fixture and returns the
// classification.
func classifyFixture(t *testing.T, f fixture) sweepResult {
	t.Helper()
	repoRoot := repoRoot(t)

	importPaths := make([]string, len(f.ImportPaths))
	for i, p := range f.ImportPaths {
		importPaths[i] = filepath.Join(repoRoot, p)
	}

	oldResolver := protocompile.WithStandardImports(&protocompile.SourceResolver{
		ImportPaths: importPaths,
	})
	newResolver := &protocompile.SourceResolver{
		ImportPaths: importPaths,
	}

	oldCompiler := dualcompiler.NewOldCompiler(dualcompiler.WithResolver(oldResolver))
	newCompiler := dualcompiler.NewNewCompiler(dualcompiler.WithResolver(newResolver))

	ctx := context.Background()

	oldResult, oldErr := compileOne(ctx, oldCompiler, f.File)
	newResult, newErr := compileOne(ctx, newCompiler, f.File)

	switch {
	case oldErr == nil && newErr == nil:
		oldFdp, err1 := oldResult.FileDescriptorProto()
		newFdp, err2 := newResult.FileDescriptorProto()
		if err1 != nil || err2 != nil {
			return sweepResult{Path: f.Path, Category: "ERROR", Notes: joinErrs(err1, err2)}
		}

		oldStripped := stripVolatile(oldFdp)
		newStripped := stripVolatile(newFdp)

		if diff := cmp.Diff(oldStripped, newStripped, protocmp.Transform()); diff != "" {
			return sweepResult{
				Path:     f.Path,
				Category: "BOTH_OK_DIFFER",
				Notes:    summarizeDiff(diff),
			}
		}
		return sweepResult{Path: f.Path, Category: "BOTH_OK_MATCH"}

	case oldErr == nil && newErr != nil:
		return sweepResult{Path: f.Path, Category: "NEW_FAIL", Notes: oneLine(newErr.Error())}

	case oldErr != nil && newErr == nil:
		return sweepResult{Path: f.Path, Category: "OLD_FAIL", Notes: oneLine(oldErr.Error())}

	default:
		return sweepResult{Path: f.Path, Category: "BOTH_FAIL", Notes: oneLine(oldErr.Error())}
	}
}

func compileOne(ctx context.Context, c dualcompiler.CompilerInterface, file string) (dualcompiler.CompiledFile, error) {
	result, err := c.Compile(ctx, file)
	if err != nil {
		return nil, err
	}
	files := result.Files()
	if len(files) == 0 {
		return nil, errors.New("no files in result")
	}
	// The fixture's compiled file should be one of the returned files —
	// the resolver may also pull in dependencies but the fixture itself
	// is what we want to diff. Look up by basename match.
	want := filepath.Base(file)
	for _, cf := range files {
		if filepath.Base(cf.Path()) == want {
			return cf, nil
		}
	}
	return files[0], nil
}

// stripVolatile removes fields that are expected to differ between
// pipelines for reasons unrelated to semantic correctness. Today that
// is just SourceCodeInfo: the experimental compiler computes it
// differently and the existing dualcompiler comparison strips it by
// default.
func stripVolatile(fdp *descriptorpb.FileDescriptorProto) *descriptorpb.FileDescriptorProto {
	clone := proto.CloneOf(fdp)
	clone.SourceCodeInfo = nil
	return clone
}

func joinErrs(errs ...error) string {
	var parts []string
	for _, e := range errs {
		if e != nil {
			parts = append(parts, oneLine(e.Error()))
		}
	}
	return strings.Join(parts, " | ")
}

func oneLine(s string) string {
	s = pointerRegex.ReplaceAllString(s, "<ptr>")
	s = absPathRegex.ReplaceAllString(s, "<repo>/")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return strings.TrimSpace(s)
}

func summarizeDiff(diff string) string {
	// Take the count of `-` and `+` lines as a rough measure of how
	// many fields differ. Surface the first divergent field path so the
	// reader can investigate.
	lines := strings.Split(diff, "\n")
	var minus, plus int
	firstField := ""
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, "-"):
			minus++
		case strings.HasPrefix(l, "+"):
			plus++
		}
		if firstField == "" {
			trim := strings.TrimSpace(l)
			if idx := strings.Index(trim, ":"); idx > 0 && !strings.HasPrefix(trim, "-") && !strings.HasPrefix(trim, "+") {
				firstField = trim[:idx]
			}
		}
	}
	if firstField == "" {
		return fmt.Sprintf("%d-/%d+ lines", minus, plus)
	}
	return fmt.Sprintf("%d-/%d+ lines; first field: %s", minus, plus, firstField)
}

func renderReport(results []sweepResult) string {
	counts := map[string]int{}
	for _, r := range results {
		counts[r.Category]++
	}

	var b strings.Builder
	b.WriteString("# Sweep report: legacy vs experimental compiler\n")
	b.WriteString("#\n")
	b.WriteString("# Generated by TestSweep. Refresh with:\n")
	b.WriteString("#   PROTOCOMPILE_REFRESH=1 go test ./internal/testing/dualcompiler -run TestSweep\n")
	b.WriteString("#\n")
	b.WriteString("# Categories:\n")
	b.WriteString("#   BOTH_OK_MATCH  - both pipelines produced identical descriptors\n")
	b.WriteString("#   BOTH_OK_DIFFER - both succeeded but descriptors differ\n")
	b.WriteString("#   OLD_FAIL       - legacy pipeline failed; experimental succeeded\n")
	b.WriteString("#   NEW_FAIL       - experimental pipeline failed; legacy succeeded\n")
	b.WriteString("#   BOTH_FAIL      - both pipelines failed (expected for malformed fixtures)\n")
	b.WriteString("#   ERROR          - harness failed to evaluate the result\n")
	b.WriteString("\n")

	b.WriteString("## Summary\n")
	fmt.Fprintf(&b, "total: %d\n", len(results))
	for _, cat := range []string{"BOTH_OK_MATCH", "BOTH_OK_DIFFER", "OLD_FAIL", "NEW_FAIL", "BOTH_FAIL", "ERROR"} {
		if counts[cat] > 0 {
			fmt.Fprintf(&b, "%-15s %d\n", cat+":", counts[cat])
		}
	}
	b.WriteString("\n")

	b.WriteString("## Per-fixture\n")
	for _, r := range results {
		if r.Notes == "" {
			fmt.Fprintf(&b, "%s %s\n", r.Path, r.Category)
		} else {
			fmt.Fprintf(&b, "%s %s | %s\n", r.Path, r.Category, r.Notes)
		}
	}
	return b.String()
}

// testReporter is the minimal subset of [testing.TB] that
// [assertMustMatch] needs. Pulling it out into a tiny interface lets
// a sibling test substitute a mock and confirm the gate fires on a
// synthetic regression — [testing.TB] itself has an unexported
// private() method, so external code cannot satisfy it.
type testReporter interface {
	Helper()
	Errorf(format string, args ...any)
}

// assertMustMatch fails the test if any fixture in [mustMatch] does
// not classify as BOTH_OK_MATCH. The failure message lists every
// regression in one shot so the reader can act on the full picture
// without re-running.
func assertMustMatch(t testReporter, results []sweepResult) {
	t.Helper()

	byPath := make(map[string]sweepResult, len(results))
	for _, r := range results {
		byPath[r.Path] = r
	}

	var regressions []string
	var missing []string
	for _, path := range mustMatch {
		r, ok := byPath[path]
		if !ok {
			missing = append(missing, path)
			continue
		}
		if r.Category != "BOTH_OK_MATCH" {
			note := r.Category
			if r.Notes != "" {
				note += " | " + r.Notes
			}
			regressions = append(regressions, fmt.Sprintf("%s → %s", path, note))
		}
	}

	if len(missing) > 0 {
		t.Errorf("mustMatch fixtures missing from sweep (the fixture moved or "+
			"was deleted; update mustMatch in this file): %v", missing)
	}
	if len(regressions) > 0 {
		t.Errorf("equivalence regression: the following fixtures must classify "+
			"as BOTH_OK_MATCH but did not:\n  %s",
			strings.Join(regressions, "\n  "))
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	// This file lives at internal/testing/dualcompiler/sweep_test.go.
	// Walk three levels up to reach the repo root.
	wd, err := os.Getwd()
	require.NoError(t, err)
	for range 3 {
		wd = filepath.Dir(wd)
	}
	return wd
}
