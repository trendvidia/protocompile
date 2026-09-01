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

package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The collide package's fixtures are reused rather than duplicated; this
// binary is a thin shell over it and what needs covering here is the exit
// codes and the report, not the detection.
const (
	chameleon    = "chameleon=../../collide/testdata/chameleon"
	voyaClean    = "voya=../../collide/testdata/voya"
	voyaVendored = "voya=../../collide/testdata/voya_vendored"
)

// TestExitsNonZeroAndNamesBothClaimants is issue #139's first acceptance
// criterion: two modules that both define pxf/annotations.proto, exiting
// non-zero and naming both claimants, the colliding path, and at least one
// colliding fully-qualified name.
func TestExitsNonZeroAndNamesBothClaimants(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	code := run(t.Context(), []string{chameleon, voyaVendored}, &stdout, &stderr)

	assert.Equal(t, exitCollided, code)
	out := stdout.String()
	assert.Contains(t, out, `file "pxf/annotations.proto"`, "the colliding path")
	assert.Contains(t, out, "pxf.required", "a colliding fully-qualified name")
	assert.Contains(t, out, "chameleon", "first claimant")
	assert.Contains(t, out, "voya", "second claimant")
	assert.Contains(t, out, "would panic in protoregistry at init")
	assert.Empty(t, stderr.String())
}

// TestExitsZeroWhenOnlyOneDefinesIt is the second criterion, and the shape
// the voya/chameleon constraint takes as a CI check: voya links chameleon
// and does not vendor its own copy.
func TestExitsZeroWhenOnlyOneDefinesIt(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	code := run(t.Context(), []string{chameleon, voyaClean}, &stdout, &stderr)

	assert.Equal(t, exitOK, code)
	assert.Contains(t, stdout.String(), "no collisions")
	assert.Empty(t, stderr.String())
}

// TestQuietReportsThroughExitCodeOnly keeps -q usable as a bare CI gate.
func TestQuietReportsThroughExitCodeOnly(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	code := run(t.Context(), []string{"-q", chameleon, voyaVendored}, &stdout, &stderr)

	assert.Equal(t, exitCollided, code)
	assert.Empty(t, stdout.String())
	assert.Empty(t, stderr.String())
}

// TestUnreadableModuleIsDistinctFromACollision keeps a broken run from
// looking like a clean one, and from looking like a collision either.
func TestUnreadableModuleIsDistinctFromACollision(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	code := run(t.Context(), []string{chameleon, "voya=../../collide/testdata/nope"}, &stdout, &stderr)

	assert.Equal(t, exitError, code)
	assert.Contains(t, stderr.String(), "does not exist")
	assert.Empty(t, stdout.String())
}

func TestArgumentErrors(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "one module", args: []string{chameleon}, want: "at least two modules"},
		{name: "no equals", args: []string{chameleon, "justadir"}, want: "not of the form"},
		{name: "empty name", args: []string{chameleon, "=dir"}, want: "not of the form"},
		{name: "empty dir", args: []string{chameleon, "name="}, want: "not of the form"},
		{name: "none", args: nil, want: "at least two modules"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			code := run(t.Context(), tc.args, &stdout, &stderr)
			assert.Equal(t, exitError, code)
			assert.Contains(t, stderr.String(), tc.want)
			// Usage follows the error, so an operator sees the fix.
			assert.Contains(t, stderr.String(), "Usage:")
		})
	}
}

func TestPlural(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "1 file path", plural(1, "file path"))
	assert.Equal(t, "2 file paths", plural(2, "file path"))
	assert.Equal(t, "0 symbols", plural(0, "symbol"))
}

func TestUsageMentionsExitCodes(t *testing.T) {
	t.Parallel()
	require.Contains(t, usage, "Exit codes:",
		"the usage text is where an operator learns what 1 and 2 mean")
}
