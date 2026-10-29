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

package fdp_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/trendvidia/protocompile/fdp"
	"github.com/trendvidia/protocompile/incremental"
	"github.com/trendvidia/protocompile/incremental/queries"
	"github.com/trendvidia/protocompile/ir"
	"github.com/trendvidia/protocompile/source"
)

// TestLeadingAnnotationComments checks that a doc comment written above a
// run of leading annotation uses is the declaration's leading comment in
// SourceCodeInfo, for each declaration kind that takes leading annotations
// (#259), and that the location's span stays the declaration's own.
func TestLeadingAnnotationComments(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

annotation note(text: string);

// Plain message.
message Plain {}

// Detached above the run.

// Above the run.
@note("a") @note("b")
message Annotated {
  // Above the oneof.
  @note("c")
  oneof choice {
    string s = 1;
  }

  // Above the field.
  string f = 2 @note("d");
}

@note("e")
// Between the run and the keyword.
message OnlyBetween {}

// Above wins.
@note("f")
// Between loses.
message Both {}

// Above the enum.
@note("g")
enum E {
  // Above the value.
  E_ZERO = 0 @note("h");
}

// Above the service.
@note("i")
service S {
  // Above the rpc.
  @note("j")
  rpc Get(Plain) returns (Plain);
}
`
	f := compileWithSourceInfo(t, src)

	type loc struct {
		leading  string
		detached []string
		line     int32
	}
	locs := map[string]loc{}
	for _, l := range f.GetSourceCodeInfo().GetLocation() {
		locs[fmt.Sprint(l.GetPath())] = loc{l.GetLeadingComments(), l.GetLeadingDetachedComments(), l.GetSpan()[0]}
	}

	for _, tt := range []struct {
		name, path, leading string
		detached            []string
		line                int32 // 0-based line where the declaration's own span starts
	}{
		{"plain message", "[4 0]", " Plain message.\n", nil, 6},
		{"message", "[4 1]", " Above the run.\n", []string{" Detached above the run.\n"}, 12},
		{"oneof", "[4 1 8 0]", " Above the oneof.\n", nil, 15},
		{"field with trailing annotation", "[4 1 2 1]", " Above the field.\n", nil, 20},
		{"comment only between run and keyword", "[4 2]", " Between the run and the keyword.\n", nil, 25},
		{"comment above the run wins", "[4 3]", " Above wins.\n", nil, 30},
		{"enum", "[5 0]", " Above the enum.\n", nil, 34},
		{"enum value with trailing annotation", "[5 0 2 0]", " Above the value.\n", nil, 36},
		{"service", "[6 0]", " Above the service.\n", nil, 41},
		{"rpc", "[6 0 2 0]", " Above the rpc.\n", nil, 44},
	} {
		got, ok := locs[tt.path]
		require.True(t, ok, "%s: no location at %s", tt.name, tt.path)
		assert.Equal(t, tt.leading, got.leading, "%s: leading comment", tt.name)
		assert.Equal(t, tt.detached, got.detached, "%s: detached comments", tt.name)
		assert.Equal(t, tt.line, got.line, "%s: span start line", tt.name)
	}
}

// compileWithSourceInfo compiles a single .proto source and returns its
// FileDescriptorProto with SourceCodeInfo.
func compileWithSourceInfo(t *testing.T, src string) *descriptorpb.FileDescriptorProto {
	t.Helper()
	opener := source.NewMap(map[string]*source.File{
		"x.proto": source.NewFile("x.proto", src),
	})
	results, rep, err := incremental.Run(t.Context(), incremental.New(), queries.IR{
		Opener:  &source.Openers{opener, source.WKTs()},
		Session: new(ir.Session),
		Path:    "x.proto",
	})
	require.NoError(t, err)
	requireNoErrors(t, rep)
	require.Len(t, results, 1)

	out, err := fdp.DescriptorProto(results[0].Value, fdp.IncludeSourceCodeInfo(true))
	require.NoError(t, err)
	return out
}
