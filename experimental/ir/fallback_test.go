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

package ir

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFallbackDescriptorProto verifies that [Session.fallbackDescriptorProto]
// lazily parses, lowers, and populates [File.dpBuiltins] from the baked-in
// well-known `google/protobuf/descriptor.proto`.
func TestFallbackDescriptorProto(t *testing.T) {
	t.Parallel()

	s := new(Session)
	f := s.fallbackDescriptorProto()

	require.NotNil(t, f, "fallback file must not be nil")
	require.Equal(t, DescriptorProtoPath, f.Path(),
		"fallback file must carry the canonical descriptor.proto path so IsDescriptorProto() returns true")
	require.True(t, f.IsDescriptorProto(),
		"fallback file must satisfy IsDescriptorProto()")
	require.NotNil(t, f.dpBuiltins,
		"fallback file must have dpBuiltins populated")

	// Smoke-check the required builtins that user-supplied partial vendored
	// descriptor.protos commonly omit (the *.options Member chain) and at
	// least one field member on a *Options type.
	b := f.dpBuiltins
	assert.False(t, b.FileOptions.IsZero(), "FileOptions Member should be populated")
	assert.False(t, b.MessageOptions.IsZero(), "MessageOptions Member should be populated")
	assert.False(t, b.FieldOptions.IsZero(), "FieldOptions Member should be populated")
	assert.False(t, b.EnumOptions.IsZero(), "EnumOptions Member should be populated")
	assert.False(t, b.JSONName.IsZero(), "JSONName Member should be populated")
	assert.False(t, b.MapEntry.IsZero(), "MapEntry Member should be populated")
}

// TestFallbackDescriptorProto_OnceSemantics confirms that repeated calls
// return the same file pointer (sync.Once semantics).
func TestFallbackDescriptorProto_OnceSemantics(t *testing.T) {
	t.Parallel()

	s := new(Session)
	f1 := s.fallbackDescriptorProto()
	f2 := s.fallbackDescriptorProto()
	require.Same(t, f1, f2, "fallbackDescriptorProto must be a one-shot lazy initialiser")
}
