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
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/trendvidia/protocompile/internal/descsrc"
	"github.com/trendvidia/protocompile/source"
)

// TestNestedTypeOrderingSweep renders every message body of length <= 3
// drawn from the declaration kinds that interact with nested_type ordering,
// and requires each to round-trip.
//
// The bug this exists to catch does not need an exotic descriptor — it needs
// three ordinary declarations in a particular order, and the combination is
// what makes it. A group-less `extend` block ahead of a group-declaring one
// stranded the second behind the fields, reordering nested_type silently.
// Neither the corpus nor any hand-written fixture in this package found it;
// enumerating the combinations did, immediately.
//
// Kept small on purpose. Four kinds at length 3 is 84 bodies and about half
// a second, ten under `-race`; the seven-kind, length-4 version is 2800
// bodies and forty seconds, which is not worth carrying to find the same
// root causes.
func TestNestedTypeOrderingSweep(t *testing.T) {
	t.Parallel()

	kinds := []func(i int) string{
		func(i int) string {
			return fmt.Sprintf("extend Bar { optional group H%d = %d { optional int32 x = 1; } }", i, i+1)
		},
		func(i int) string { return fmt.Sprintf("extend Foo { optional int32 p%d = %d; }", i, i+1) },
		func(i int) string { return fmt.Sprintf("map<int32, string> m%d = %d;", i, i+50) },
		func(i int) string { return fmt.Sprintf("message N%d { optional int32 n = 1; }", i) },
	}

	var bodies [][]int
	var rec func(cur []int)
	rec = func(cur []int) {
		if len(cur) > 0 {
			bodies = append(bodies, append([]int(nil), cur...))
		}
		if len(cur) == 3 {
			return
		}
		for k := range kinds {
			rec(append(cur, k))
		}
	}
	rec(nil)

	var ok, refused, diverged int
	for _, body := range bodies {
		var sb strings.Builder
		for pos, k := range body {
			sb.WriteString("  " + kinds[k](pos) + "\n")
		}
		src := "syntax = \"proto2\";\nmessage Foo { extensions 1 to 100; }\nmessage Bar { extensions 1 to 100; }\nmessage M {\n" + sb.String() + "}\n"

		openerFor := func(text string) source.Opener {
			return &source.Openers{
				source.NewMap(map[string]*source.File{"t.proto": source.NewFile("t.proto", text)}),
				source.WKTs(),
			}
		}
		want, err := compile(t, openerFor(src), "t.proto")
		if err != nil {
			continue
		}
		rendered, err := descsrc.Render(want)
		if err != nil {
			refused++
			continue
		}
		got, err := compile(t, openerFor(rendered), "t.proto")
		if err != nil {
			diverged++
			t.Errorf("rendered does not compile:\n%s\n%v", src, err)
			continue
		}
		w, _ := proto.Clone(want).(*descriptorpb.FileDescriptorProto)
		g, _ := proto.Clone(got).(*descriptorpb.FileDescriptorProto)
		w.SourceCodeInfo, g.SourceCodeInfo = nil, nil
		if cmp.Diff(w, g, protocmp.Transform()) != "" {
			diverged++
			if diverged <= 2 {
				t.Errorf("DIVERGED:\n%s", src)
			}
			continue
		}
		ok++
	}
	t.Logf("bodies=%d ok=%d refused=%d diverged=%d", len(bodies), ok, refused, diverged)
	assert.Zero(t, diverged, "every body must round-trip")
}
