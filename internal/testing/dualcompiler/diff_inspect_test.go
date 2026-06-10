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
	"bytes"
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/google/go-cmp/cmp"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"

	"github.com/trendvidia/protocompile"
	"github.com/trendvidia/protocompile/internal/testing/dualcompiler"
)

// TestDiffInspect is a debug-only helper that prints the full diff for
// a single fixture. Run it with a fixture path supplied via the env
// var PROTOCOMPILE_INSPECT to see the exact descriptor divergence
// between the two pipelines. Skipped when the env var is unset.
//
//	PROTOCOMPILE_INSPECT=desc_test_defaults.proto \
//	  go test ./internal/testing/dualcompiler -run TestDiffInspect -v
//
// This is a companion to TestSweep, which classifies divergences across
// the whole fixture corpus into a single golden report. When the sweep
// reports a fixture as BOTH_OK_DIFFER and the report's one-line note
// isn't enough to act on, the inspect tool prints the full diff so the
// reader can decide whether the divergence is benign (e.g. formatting
// variance) or a real semantic gap to close.
func TestDiffInspect(t *testing.T) {
	fixture := getInspectFixture(t)
	if fixture == "" {
		t.Skip("set PROTOCOMPILE_INSPECT to a fixture path to see its diff")
	}

	repoRoot := repoRoot(t)
	importPaths := []string{
		repoRoot + "/internal/testdata",
		repoRoot + "/internal/testdata/options",
	}

	oldR := protocompile.WithStandardImports(&protocompile.SourceResolver{ImportPaths: importPaths})
	newR := &protocompile.SourceResolver{ImportPaths: importPaths}

	oldC := dualcompiler.NewOldCompiler(dualcompiler.WithResolver(oldR))
	newC := dualcompiler.NewNewCompiler(dualcompiler.WithResolver(newR))

	ctx := context.Background()
	oldRes, oldErr := oldC.Compile(ctx, fixture)
	newRes, newErr := newC.Compile(ctx, fixture)

	if oldErr != nil {
		t.Logf("old failed: %v", oldErr)
	}
	if newErr != nil {
		t.Logf("new failed: %v", newErr)
	}
	if oldErr != nil || newErr != nil {
		return
	}

	for i := range oldRes.Files() {
		oldFdp, _ := oldRes.Files()[i].FileDescriptorProto()
		newFdp, _ := newRes.Files()[i].FileDescriptorProto()
		oldClone := stripVolatile(oldFdp)
		newClone := stripVolatile(newFdp)
		if diff := cmp.Diff(oldClone, newClone, protocmp.Transform()); diff != "" {
			fmt.Printf("=== %s ===\n%s\n", oldRes.Files()[i].Path(), diff)
		}

		oldBytes, _ := proto.Marshal(oldClone)
		newBytes, _ := proto.Marshal(newClone)
		fmt.Printf("=== %s: marshaled bytes old=%d new=%d byte-match=%v proto-equal=%v ===\n",
			oldRes.Files()[i].Path(),
			len(oldBytes), len(newBytes),
			bytes.Equal(oldBytes, newBytes),
			proto.Equal(oldClone, newClone))
	}
}

func getInspectFixture(t *testing.T) string {
	t.Helper()
	return os.Getenv("PROTOCOMPILE_INSPECT")
}
