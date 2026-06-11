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

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/trendvidia/protocompile/internal/testing/protoctest"
)

var pointerRegex = regexp.MustCompile(`0x[0-9a-f]+`)

var absPathRegex = regexp.MustCompile(`/[^ :]*/protocompile/`)

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
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for range 3 {
		wd = filepath.Dir(wd)
	}
	return wd
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
