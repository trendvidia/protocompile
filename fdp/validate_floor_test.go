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
	"regexp"
	"runtime/debug"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// validateProtosModule is the BSR-generated Go code for the buf.validate
// protos. fdp links it through buf/descriptor/v1, and this module's
// go.mod records the requirement as a floor every consumer inherits.
const validateProtosModule = "buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go"

// minValidateProtos is the proto commit time, not the version, of the
// oldest build the floor may name: the one buf.build/go/protovalidate
// v1.2.0 requires. Older builds lack FieldPath, Rule and the other types
// that release compiles against.
const minValidateProtos = "20260415201107"

// TestValidateProtosFloorIsNotStale pins issue #243. A BSR pseudo-version
// leads with the protobuf-go version it was generated with, so the 2024
// build at v1.36.12 sorts above the 2026 build at v1.36.11, and minimal
// version selection hands a consumer of protovalidate v1.2.0 the older
// protos. The comparison therefore reads the proto timestamp inside the
// version rather than trusting version order.
func TestValidateProtosFloorIsNotStale(t *testing.T) {
	t.Parallel()

	info, ok := debug.ReadBuildInfo()
	require.True(t, ok, "test binary carries no build info")

	var version string
	for _, dep := range info.Deps {
		if dep.Path == validateProtosModule {
			version = dep.Version
			if dep.Replace != nil {
				version = dep.Replace.Version
			}
		}
	}
	require.NotEmptyf(t, version, "%s is not linked into fdp; if that is deliberate, delete this test", validateProtosModule)

	stamp, ok := bsrTimestamp(version)
	require.Truef(t, ok, "%s@%s is not a BSR pseudo-version; the floor check cannot read its proto timestamp", validateProtosModule, version)
	assert.GreaterOrEqualf(t, stamp, minValidateProtos,
		"%s@%s carries buf.validate protos from %s, older than the %s build protovalidate v1.2.0 needs. "+
			"Consumers inherit this floor, and version order prefers it over newer protos at a lower protobuf-go version. "+
			"Raise it with go get. See issue #243.",
		validateProtosModule, version, stamp, minValidateProtos)
}

// bsrPseudoVersion matches a BSR generated-SDK version:
// v<plugin version>-<proto commit time>-<commit>.<revision>.
var bsrPseudoVersion = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+-([0-9]{14})-[0-9a-f]{12}\.[0-9]+$`)

// bsrTimestamp returns the proto commit time inside a BSR version,
// reporting whether version has that shape.
func bsrTimestamp(version string) (string, bool) {
	m := bsrPseudoVersion.FindStringSubmatch(version)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// TestBSRTimestamp pins the version parsing the floor check relies on,
// including the ordering trap from issue #243: the later proto build
// carries the lower plugin version.
func TestBSRTimestamp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		version string
		want    string
		wantOK  bool
	}{
		{version: "v1.36.12-20240920164238-5a7b106cbb87.1", want: "20240920164238", wantOK: true},
		{version: "v1.36.11-20260415201107-50325440f8f2.1", want: "20260415201107", wantOK: true},
		{version: "v1.36.12-20260825204119-511051f7f437.2", want: "20260825204119", wantOK: true},
		{version: "v1.36.12", wantOK: false},
		{version: "v0.0.0-20260113132338-7c7de50cc741", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			t.Parallel()
			got, ok := bsrTimestamp(tt.version)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.want, got)
		})
	}

	older, _ := bsrTimestamp("v1.36.12-20240920164238-5a7b106cbb87.1")
	newer, _ := bsrTimestamp("v1.36.11-20260415201107-50325440f8f2.1")
	assert.Less(t, older, newer, "proto timestamps must order by time even where versions order the other way")
}
