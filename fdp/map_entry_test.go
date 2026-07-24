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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protodesc"
)

// TestMapEntryOptionsTyped verifies the synthetic map-entry message
// carries options.map_entry = true as the TYPED field, not as
// unknown-field bytes (issue #110). Reflection consumers of the
// in-memory FDP — protodesc.NewFile's IsMap classification,
// protoreflect map accessors, dynamicpb — read the typed field;
// unknown-byte options only materialize after a wire round-trip.
func TestMapEntryOptionsTyped(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package coll;

message Book {
  map<string, string> tags = 1;
}
`
	f := compileForFDPTest(t, src)
	require.Len(t, f.GetMessageType(), 1)
	book := f.GetMessageType()[0]
	require.Len(t, book.GetNestedType(), 1)
	entry := book.GetNestedType()[0]
	assert.Equal(t, "TagsEntry", entry.GetName())

	require.NotNil(t, entry.Options)
	assert.True(t, entry.Options.GetMapEntry(),
		"map_entry must be set on the typed field, not unknown bytes")
	assert.Empty(t, entry.Options.ProtoReflect().GetUnknown(),
		"no residual unknown-byte encoding of the option")

	fd, err := protodesc.NewFile(f, nil)
	require.NoError(t, err)
	field := fd.Messages().Get(0).Fields().Get(0)
	assert.True(t, field.IsMap(), "reflection must classify the field as a map")
	assert.False(t, field.IsList())
}
