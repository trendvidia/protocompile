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

// Helpers carried over from the (now-deleted) sweep_test.go that
// compared legacy↔experimental — Track C removed the legacy pipeline,
// so the legacy↔experimental comparison was retired, but
// TestSweepVsProtoc (in sweep_protoc_test.go) still uses these
// fixture-collection helpers.

package protoctest_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/trendvidia/protocompile/internal/testing/protoctest"
)

var pointerRegex = regexp.MustCompile(`0x[0-9a-f]+`)

// The optional `[A-Za-z]:` is a Windows drive letter. Without it the
// pattern starts at `/` and `[^ :]*` cannot cross the colon, so a path
// protoc printed as `C:/Users/.../protocompile/...` had everything after
// the drive scrubbed and `C:` left stranded in the report (#197).
var absPathRegex = regexp.MustCompile(`(?:[A-Za-z]:)?/[^ :]*/protocompile/`)

// sweepRepoRoot is the checkout root, found from this package's location
// rather than from the checkout's name. absPathRegex alone scrubs a path
// only when the directory above it is called "protocompile", so in a git
// worktree or any CI checkout under another name the absolute paths in
// protoc's diagnostics survived into the report and drifted it against the
// golden — a failure that says nothing about the compiler.
var sweepRepoRoot = func() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	// This package sits at internal/testing/protoctest.
	for range 3 {
		wd = filepath.Dir(wd)
	}
	return wd
}()

type fixture struct {
	Path        string
	ImportPaths []string
	File        string
}

type sweepResult struct {
	Path     string
	Category string
	Notes    string
}

// testReporter is the minimal subset of [testing.TB] that the
// mustMatch gates need. Pulling it out into a tiny interface lets a
// future sibling test substitute a mock; [testing.TB] itself has an
// unexported private() method so external code cannot satisfy it.
type testReporter interface {
	Helper()
	Errorf(format string, args ...any)
}

// fixturesSkippedFromSweep names fixtures whose protoc-vs-protocompile
// classification depends on the protobuf-go runtime build tag (today
// only `protolegacy`, which gates message-set wire format support).
// They are excluded from the sweep so the golden is stable across CI's
// two test passes (`make test` runs with and without the tag).
//
// The fixture files themselves stay on disk for reference and manual
// reproduction.
var fixturesSkippedFromSweep = map[string]string{
	"internal/testdata/protobuf/google/protobuf/unittest_custom_options.proto": "uses message_set_wire_format; outcome depends on the `protolegacy` build tag",
}

func collectFixtures(t *testing.T) []fixture {
	t.Helper()

	rootDir := repoRoot(t)

	roots := []struct {
		dir         string
		importPaths []string
	}{
		{dir: "internal/testdata", importPaths: []string{"internal/testdata"}},
		{dir: "internal/testdata/options", importPaths: []string{"internal/testdata", "internal/testdata/options"}},
		{dir: "internal/testdata/nopkg", importPaths: []string{"internal/testdata", "internal/testdata/nopkg"}},
		{dir: "internal/testdata/pkg", importPaths: []string{"internal/testdata", "internal/testdata/pkg"}},
		// Fixtures vendored from protocolbuffers/protobuf's own
		// parser test corpus. Cross-imports use `google/protobuf/X.proto`
		// paths, so the import root is the vendor-tree top
		// (`internal/testdata/protobuf`), not the leaf directory.
		{dir: "internal/testdata/protobuf/google/protobuf", importPaths: []string{"internal/testdata/protobuf"}},
		// Fixtures exercising PSE schema-extension grammar
		// (`annotation` declarations + `@name(args)` use sites).
		// protoc rejects this grammar entirely, so the canonical
		// outcome for every fixture under here is PROTOC_FAIL.
		// Anything else (BOTH_FAIL, PROTOC_MATCH, PROTOC_DIFFER)
		// is a regression and will fail the golden-diff check.
		{dir: "internal/testdata/annotations", importPaths: []string{"internal/testdata/annotations"}},
	}

	var out []fixture
	for _, r := range roots {
		entries, err := os.ReadDir(filepath.Join(rootDir, r.dir))
		if err != nil {
			t.Fatalf("read %s: %v", r.dir, err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".proto") {
				continue
			}
			path := filepath.ToSlash(filepath.Join(r.dir, e.Name()))
			if _, skip := fixturesSkippedFromSweep[path]; skip {
				continue
			}

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

func repoRoot(t *testing.T) string {
	t.Helper()
	if sweepRepoRoot == "" {
		t.Fatal("getwd failed; cannot locate the repository root")
	}
	return sweepRepoRoot
}

func compileOne(ctx context.Context, c protoctest.Compiler, file string) (protoctest.CompiledFile, error) {
	result, err := c.Compile(ctx, file)
	if err != nil {
		return nil, err
	}
	files := result.Files()
	if len(files) == 0 {
		return nil, errors.New("no files in result")
	}
	want := filepath.Base(file)
	for _, cf := range files {
		if filepath.Base(cf.Path()) == want {
			return cf, nil
		}
	}
	return files[0], nil
}

// stripVolatile removes fields that are expected to differ for
// reasons unrelated to semantic correctness. Today that is just
// SourceCodeInfo.
func stripVolatile(fdp *descriptorpb.FileDescriptorProto) *descriptorpb.FileDescriptorProto {
	clone := proto.CloneOf(fdp)
	clone.SourceCodeInfo = nil
	return clone
}

func oneLine(s string) string {
	s = pointerRegex.ReplaceAllString(s, "<ptr>")
	if sweepRepoRoot != "" {
		s = strings.ReplaceAll(s, sweepRepoRoot+string(filepath.Separator), "<repo>/")
		// protoc prints forward slashes on Windows even though
		// filepath.Separator there is a backslash, so the replacement
		// above never fires and the path reaches absPathRegex instead.
		// Handled here so the report does not depend on which of the two
		// happens to match.
		s = strings.ReplaceAll(s, filepath.ToSlash(sweepRepoRoot)+"/", "<repo>/")
	}
	s = absPathRegex.ReplaceAllString(s, "<repo>/")
	// protoc's stderr is CRLF on Windows. Dropping \r keeps the golden
	// report identical across platforms rather than one carriage return
	// per diagnostic different.
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "")
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

// TestOneLineIsPlatformIndependent pins the normalisation the sweep report
// depends on, using synthetic input so it runs everywhere rather than only
// where the platform in question is.
//
// Both cases below drifted the report on Windows the first time the lane
// ran on self-hosted hardware (#197): the drive letter survived because
// absPathRegex could not cross the colon, and protoc's CRLF stderr left a
// carriage return in every diagnostic.
func TestOneLineIsPlatformIndependent(t *testing.T) {
	t.Parallel()

	const want = "<repo>/internal/testdata/a.proto:12:1: Expected top-level statement."
	for _, tc := range []struct{ name, in string }{
		{
			"unix absolute path",
			"/home/runner/work/protocompile/internal/testdata/a.proto:12:1: Expected top-level statement.",
		},
		{
			"windows drive letter",
			"C:/Users/runner/work/protocompile/internal/testdata/a.proto:12:1: Expected top-level statement.",
		},
		{
			"windows drive letter, lowercase",
			"d:/a/protocompile/internal/testdata/a.proto:12:1: Expected top-level statement.",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, want, oneLine(tc.in))
		})
	}

	t.Run("crlf collapses like lf", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, oneLine("one\ntwo"), oneLine("one\r\ntwo"))
		assert.NotContains(t, oneLine("one\r\ntwo"), "\r")
	})
}
