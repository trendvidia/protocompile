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

package descsrc_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/trendvidia/protocompile/fdp"
	"github.com/trendvidia/protocompile/incremental"
	"github.com/trendvidia/protocompile/incremental/queries"
	"github.com/trendvidia/protocompile/internal/descsrc"
	"github.com/trendvidia/protocompile/ir"
	"github.com/trendvidia/protocompile/source"
)

// repoRoot walks up from the test's working directory to the module root.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find module root")
		}
		dir = parent
	}
}

// compile lowers path to a FileDescriptorProto using opener to resolve it
// and its imports.
func compile(t *testing.T, opener source.Opener, path string) (*descriptorpb.FileDescriptorProto, error) {
	t.Helper()
	exec := incremental.New()
	results, _, err := incremental.Run(t.Context(), exec, queries.IR{
		Opener:  opener,
		Session: new(ir.Session),
		Path:    path,
	})
	if err != nil {
		return nil, err
	}
	if len(results) != 1 || results[0].Value == nil {
		return nil, errCompile
	}
	return fdp.DescriptorProto(results[0].Value)
}

var errCompile = &compileError{}

type compileError struct{}

func (*compileError) Error() string { return "compile produced no IR" }

// corpusOpener serves the sweep corpus, which is the set of .proto files the
// protoc-comparison sweep already treats as valid input.
func corpusOpener(t *testing.T) source.Opener {
	t.Helper()
	root := repoRoot(t)
	return &source.Openers{
		&source.FS{FS: os.DirFS(filepath.Join(root, "internal/testdata"))},
		&source.FS{FS: os.DirFS(filepath.Join(root, "internal/testdata/protobuf"))},
		&source.FS{FS: os.DirFS(filepath.Join(root, "internal/testdata/options"))},
		source.WKTs(),
	}
}

// corpusFiles lists the .proto files in one corpus directory, as paths
// relative to the import root that serves them.
func corpusFiles(t *testing.T, dir, importRoot string) []string {
	t.Helper()
	root := repoRoot(t)
	entries, err := os.ReadDir(filepath.Join(root, dir))
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".proto") {
			continue
		}
		rel, err := filepath.Rel(importRoot, filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, rel)
	}
	return out
}

// TestRoundTrip is the oracle for this package.
//
// For every file in the sweep corpus it compiles the source to a descriptor,
// renders that descriptor back to source, compiles the rendered source, and
// requires the two descriptors to be equal. Equality of descriptors — not
// equality of text — is the property that matters: the rendered source is
// only ever fed back to the compiler, so formatting is free to differ and
// semantics are not.
//
// A file the renderer refuses is reported separately from a file it renders
// wrongly. The first is the documented fidelity boundary; the second is a
// bug, because it means a caller would get a silently different descriptor.
func TestRoundTrip(t *testing.T) {
	t.Parallel()

	opener := corpusOpener(t)

	paths := make([]string, 0, 64)
	paths = append(paths, corpusFiles(t, "internal/testdata", "internal/testdata")...)
	paths = append(paths, corpusFiles(t,
		"internal/testdata/protobuf/google/protobuf", "internal/testdata/protobuf")...)

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			t.Parallel()

			want, err := compile(t, opener, path)
			if err != nil || want == nil {
				t.Skipf("fixture does not compile standalone: %v", err)
			}

			rendered, err := descsrc.Render(want)
			if err != nil {
				t.Skipf("refused (fidelity boundary): %v", err)
			}

			// Serve the rendered text at the original path so imports of it
			// keep resolving, with the corpus behind for its dependencies.
			overlay := &source.Openers{
				source.NewMap(map[string]*source.File{
					path: source.NewFile(path, rendered),
				}),
				opener,
			}
			got, err := compile(t, overlay, path)
			if err != nil {
				t.Fatalf("rendered source does not compile: %v\n\n--- rendered ---\n%s", err, rendered)
			}

			// source_code_info describes the text, which is expected to
			// differ; nothing downstream of an import reads it.
			want, _ = proto.Clone(want).(*descriptorpb.FileDescriptorProto)
			got, _ = proto.Clone(got).(*descriptorpb.FileDescriptorProto)
			want.SourceCodeInfo = nil
			got.SourceCodeInfo = nil

			if diff := cmp.Diff(want, got, protocmp.Transform()); diff != "" {
				t.Errorf("descriptor changed across render/recompile (-want +got):\n%s\n\n--- rendered ---\n%s",
					diff, rendered)
			}
		})
	}
}
