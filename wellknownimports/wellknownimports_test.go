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

package wellknownimports

import (
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/trendvidia/protocompile"
)

func TestWithStandardImports(t *testing.T) {
	t.Parallel()
	wellKnownImports := []string{
		"google/protobuf/any.proto",
		"google/protobuf/api.proto",
		"google/protobuf/compiler/plugin.proto",
		"google/protobuf/cpp_features.proto",
		"google/protobuf/descriptor.proto",
		"google/protobuf/duration.proto",
		"google/protobuf/empty.proto",
		"google/protobuf/field_mask.proto",
		"google/protobuf/go_features.proto",
		"google/protobuf/java_features.proto",
		"google/protobuf/source_context.proto",
		"google/protobuf/struct.proto",
		"google/protobuf/timestamp.proto",
		"google/protobuf/type.proto",
		"google/protobuf/wrappers.proto",
	}
	// make sure we can successfully compile them all
	c := protocompile.Compiler{
		Resolver: WithStandardImports(&protocompile.SourceResolver{
			Accessor: func(_ string) (io.ReadCloser, error) {
				return nil, os.ErrNotExist
			},
		}),
		RetainASTs: true,
	}
	ctx := t.Context()
	for _, name := range wellKnownImports {
		t.Log(name)
		fds, err := c.Compile(ctx, name)
		if err != nil {
			t.Errorf("failed to compile %q: %v", name, err)
			continue
		}
		if len(fds) != 1 {
			t.Errorf("Compile returned wrong number of descriptors: expecting 1, got %d", len(fds))
			continue
		}
		// The legacy `linker.Result` interface and its `AST()` method
		// were removed in Track C; just verify the compile succeeded
		// and the descriptors are usable.
		require.NotNil(t, fds[0])

		if name == "google/protobuf/descriptor.proto" {
			// Sanity-check that FeatureSet was actually compiled. The
			// extension-range *declarations* on FeatureSet are
			// retention=RETENTION_SOURCE in descriptor.proto, so they
			// are stripped from the compiled descriptor — protoc
			// behaves the same way. Consumers that need to inspect
			// declarations should read them off the source-form
			// `descriptorpb.File_google_protobuf_descriptor_proto`
			// metadata in protobuf-go, not off a freshly-compiled FDP.
			d := fds[0].FindDescriptorByName("google.protobuf.FeatureSet")
			require.NotNil(t, d)
			md, ok := d.(protoreflect.MessageDescriptor)
			require.True(t, ok)
			require.Positive(t, md.ExtensionRanges().Len(),
				"FeatureSet should have at least one extension range")
		}
	}
}

func TestCantRedefineWellKnownCustomFeature(t *testing.T) {
	t.Parallel()
	c := protocompile.Compiler{
		Resolver: WithStandardImports(&protocompile.SourceResolver{
			Accessor: protocompile.SourceAccessorFromMap(map[string]string{
				"features.proto": `
					edition = "2023";
					import "google/protobuf/descriptor.proto";
					message Custom {
						bool flag = 1;
					}
					extend google.protobuf.FeatureSet {
						// tag 1000 is declared by pb.cpp so shouldn't be allowed
						Custom custom = 1000;
					}
					`,
			}),
		}),
	}
	ctx := t.Context()
	_, err := c.Compile(ctx, "features.proto")
	// The experimental pipeline rejects this with a type-mismatch
	// diagnostic rather than the legacy "extension number 1000 must be
	// named pb.cpp" message, but the gist is the same: the user's
	// `Custom` extension collides with the WKT's declared `pb.CppFeatures`
	// at field 1000.
	require.ErrorContains(t, err, "mismatched types")
}
