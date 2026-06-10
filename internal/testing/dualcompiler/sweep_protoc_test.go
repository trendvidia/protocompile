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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/trendvidia/protocompile"
	"github.com/trendvidia/protocompile/internal/protoc"
	"github.com/trendvidia/protocompile/internal/testing/dualcompiler"
)

// protocMustMatch is the list of fixtures that the protoc-leg sweep
// has brought to PROTOC_MATCH and that future PRs must not regress.
//
// Unlike [mustMatch] (which gates the legacy↔experimental comparison),
// this list gates experimental↔protoc parity. A fixture appearing here
// means the experimental's FileDescriptorProto is byte-for-byte
// equivalent to what `protoc -o set.pb fixture.proto` produces, after
// SourceCodeInfo is stripped on both sides.
//
// Adding an entry here records a divergence as closed; removing or
// commenting an entry to silence a failure permanently records a
// regression and should not happen without reviewer sign-off.
//
// Initially empty — populate as protoc-parity is empirically
// established for each fixture. The first PR's job is just to surface
// the current divergence shape via the [sweep_protoc.txt] golden.
var protocMustMatch = []string{}

// TestSweepVsProtoc compiles every .proto fixture under the repo's
// main testdata roots through both `protoc` and the experimental
// compiler, classifies each fixture's outcome, and writes a single
// text report.
//
// The report lives at testdata/sweep_protoc.txt under this package's
// directory. Refresh it with
//
//	PROTOCOMPILE_REFRESH=1 go test ./internal/testing/dualcompiler -run TestSweepVsProtoc
//
// This is a sibling to [TestSweep] (which compares legacy ↔
// experimental). TestSweep proves intra-project equivalence; this
// test proves project ↔ reference compiler equivalence on the same
// corpus. Categories used here:
//
//	PROTOC_MATCH       - protoc and experimental produced identical descriptors
//	PROTOC_DIFFER      - both succeeded but descriptors differ
//	PROTOC_FAIL        - protoc failed; experimental succeeded
//	NEW_FAIL_VS_PROTOC - experimental failed; protoc succeeded
//	BOTH_FAIL          - both failed (expected for malformed fixtures)
//	ERROR              - harness failed to evaluate the result
//
// Skipped if the cached protoc binary is not available — run
// `make protoc` from the repo root to provision it.
func TestSweepVsProtoc(t *testing.T) {
	t.Parallel()

	rootDir := repoRoot(t)
	protocPath, err := protoc.BinaryPath(rootDir + string(filepath.Separator))
	if err != nil {
		t.Skipf("protoc binary not available: %v (run 'make protoc' from the repo root)", err)
	}

	fixtures := collectFixtures(t)
	require.NotEmpty(t, fixtures, "sweep found no fixtures")

	results := make([]sweepResult, len(fixtures))
	for i, f := range fixtures {
		results[i] = classifyFixtureVsProtoc(t, f, protocPath, rootDir)
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].Path < results[j].Path
	})

	assertProtocMustMatch(t, results)

	report := renderProtocReport(results)

	goldenPath := filepath.Join("testdata", "sweep_protoc.txt")
	if os.Getenv("PROTOCOMPILE_REFRESH") != "" {
		require.NoError(t, os.WriteFile(goldenPath, []byte(report), 0o644))
		t.Logf("refreshed %s", goldenPath)
		return
	}

	expected, err := os.ReadFile(goldenPath)
	require.NoError(t, err, "missing golden %s; refresh with PROTOCOMPILE_REFRESH=1", goldenPath)
	if got := string(expected); got != report {
		t.Errorf("sweep_protoc report drift; refresh with PROTOCOMPILE_REFRESH=1 if expected:\n%s",
			cmp.Diff(got, report))
	}
}

// classifyFixtureVsProtoc runs protoc and the experimental compiler on
// a single fixture and returns the protoc-leg classification.
func classifyFixtureVsProtoc(t *testing.T, f fixture, protocPath, rootDir string) sweepResult {
	t.Helper()

	importPaths := make([]string, len(f.ImportPaths))
	for i, p := range f.ImportPaths {
		importPaths[i] = filepath.Join(rootDir, p)
	}

	protocFdp, protocErr := runProtoc(t, protocPath, importPaths, filepath.Join(rootDir, f.Path), f.File)

	newResolver := &protocompile.SourceResolver{
		ImportPaths: importPaths,
	}
	newCompiler := dualcompiler.NewNewCompiler(dualcompiler.WithResolver(newResolver))

	ctx := context.Background()
	newResult, newErr := compileOne(ctx, newCompiler, f.File)

	switch {
	case protocErr == nil && newErr == nil:
		newFdp, err := newResult.FileDescriptorProto()
		if err != nil {
			return sweepResult{Path: f.Path, Category: "ERROR", Notes: oneLine(err.Error())}
		}
		protocStripped := stripVolatile(protocFdp)
		newStripped := stripVolatile(newFdp)
		if diff := cmp.Diff(protocStripped, newStripped, protocmp.Transform()); diff != "" {
			return sweepResult{
				Path:     f.Path,
				Category: "PROTOC_DIFFER",
				Notes:    summarizeDiff(diff),
			}
		}
		return sweepResult{Path: f.Path, Category: "PROTOC_MATCH"}

	case protocErr == nil && newErr != nil:
		return sweepResult{Path: f.Path, Category: "NEW_FAIL_VS_PROTOC", Notes: oneLine(newErr.Error())}

	case protocErr != nil && newErr == nil:
		return sweepResult{Path: f.Path, Category: "PROTOC_FAIL", Notes: oneLine(protocErr.Error())}

	default:
		return sweepResult{Path: f.Path, Category: "BOTH_FAIL", Notes: oneLine(protocErr.Error())}
	}
}

// runProtoc shells out to protoc to compile a single fixture and
// returns the resulting [FileDescriptorProto] for the target file.
// Returns a non-nil error if protoc fails to compile the fixture.
//
// The output FileDescriptorSet is written to a per-test tempdir and
// removed when the test ends; only the target file's FDP is extracted
// from the set (no --include_imports).
func runProtoc(t *testing.T, protocPath string, importPaths []string, fixtureAbs, fileForResolver string) (*descriptorpb.FileDescriptorProto, error) {
	t.Helper()
	dir := t.TempDir()
	outPath := filepath.Join(dir, "fixture.pb")

	args := []string{"-o", outPath}
	for _, ip := range importPaths {
		args = append(args, "--proto_path="+ip)
	}
	args = append(args, fixtureAbs)

	cmd := exec.Command(protocPath, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("protoc: %v: %s", err, strings.TrimSpace(string(out)))
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		return nil, fmt.Errorf("read protoc output: %w", err)
	}
	var set descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(data, &set); err != nil {
		return nil, fmt.Errorf("unmarshal protoc output: %w", err)
	}

	// Pick the FDP whose name matches the resolver path the experimental
	// receives. protoc records the path it saw on its command line
	// relative to the import root that contained it; that is exactly
	// `fileForResolver`.
	for _, fd := range set.File {
		if fd.GetName() == fileForResolver {
			return fd, nil
		}
	}
	// Fall back: pick by basename match in case protoc disambiguated
	// the path differently (e.g., chose a different import root for
	// the same fixture).
	want := filepath.Base(fileForResolver)
	for _, fd := range set.File {
		if filepath.Base(fd.GetName()) == want {
			return fd, nil
		}
	}
	if len(set.File) > 0 {
		return set.File[len(set.File)-1], nil
	}
	return nil, fmt.Errorf("protoc emitted an empty FileDescriptorSet")
}

// renderProtocReport formats the per-fixture results into the text
// shape stored at testdata/sweep_protoc.txt.
func renderProtocReport(results []sweepResult) string {
	counts := map[string]int{}
	for _, r := range results {
		counts[r.Category]++
	}

	var b strings.Builder
	b.WriteString("# Sweep report: protoc vs experimental compiler\n")
	b.WriteString("#\n")
	b.WriteString("# Generated by TestSweepVsProtoc. Refresh with:\n")
	b.WriteString("#   PROTOCOMPILE_REFRESH=1 go test ./internal/testing/dualcompiler -run TestSweepVsProtoc\n")
	b.WriteString("#\n")
	b.WriteString("# Categories:\n")
	b.WriteString("#   PROTOC_MATCH       - protoc and experimental produced identical descriptors\n")
	b.WriteString("#   PROTOC_DIFFER      - both succeeded but descriptors differ\n")
	b.WriteString("#   PROTOC_FAIL        - protoc failed; experimental succeeded\n")
	b.WriteString("#   NEW_FAIL_VS_PROTOC - experimental failed; protoc succeeded\n")
	b.WriteString("#   BOTH_FAIL          - both failed (expected for malformed fixtures)\n")
	b.WriteString("#   ERROR              - harness failed to evaluate the result\n")
	b.WriteString("\n")

	b.WriteString("## Summary\n")
	fmt.Fprintf(&b, "total: %d\n", len(results))
	for _, cat := range []string{"PROTOC_MATCH", "PROTOC_DIFFER", "PROTOC_FAIL", "NEW_FAIL_VS_PROTOC", "BOTH_FAIL", "ERROR"} {
		if counts[cat] > 0 {
			fmt.Fprintf(&b, "%-19s %d\n", cat+":", counts[cat])
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

// assertProtocMustMatch fails the test if any entry in [protocMustMatch]
// does not classify as PROTOC_MATCH in the current run. Mirrors the
// gate semantics of [assertMustMatch] for the protoc leg.
func assertProtocMustMatch(t testReporter, results []sweepResult) {
	t.Helper()
	if len(protocMustMatch) == 0 {
		return
	}
	byPath := make(map[string]sweepResult, len(results))
	for _, r := range results {
		byPath[r.Path] = r
	}
	var missing, regressions []string
	for _, path := range protocMustMatch {
		r, ok := byPath[path]
		if !ok {
			missing = append(missing, path)
			continue
		}
		if r.Category != "PROTOC_MATCH" {
			note := r.Category
			if r.Notes != "" {
				note += " | " + r.Notes
			}
			regressions = append(regressions, fmt.Sprintf("%s → %s", path, note))
		}
	}
	if len(missing) > 0 {
		t.Errorf("protocMustMatch fixtures missing from sweep (the fixture moved or "+
			"was deleted; update protocMustMatch in this file): %v", missing)
	}
	if len(regressions) > 0 {
		t.Errorf("protoc-equivalence regression: the following fixtures must classify "+
			"as PROTOC_MATCH but did not:\n  %s",
			strings.Join(regressions, "\n  "))
	}
}
