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
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// bufSourceCodeInfoExtension is the field number of buf's
// buf.descriptor.v1.SourceCodeInfoExtension on SourceCodeInfo.
const bufSourceCodeInfoExtension = 536000000

// TestBufSourceCodeInfoExtensionBytes pins the bytes of buf's
// SourceCodeInfo extension: is_syntax_unspecified (2) and the packed
// unused_dependency indexes (3). The expected values were captured from the
// output fdp produced through buf's generated types, before it built the
// extension type itself (#266), so this shows the output is byte-identical.
// An empty extension is omitted entirely, and a present one is the first
// field of SourceCodeInfo, where Go marshals extensions.
func TestBufSourceCodeInfoExtensionBytes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, src string
		want      string // hex of the extension's value; "" means absent
	}{
		{
			name: "neither",
			src:  "syntax = \"proto3\";\npackage p;\nmessage M {}\n",
			want: "",
		},
		{
			name: "no syntax",
			src:  "package p;\nmessage M {}\n",
			want: "1001",
		},
		{
			name: "unused imports",
			src: "syntax = \"proto3\";\npackage p;\n" +
				"import \"google/protobuf/empty.proto\";\n" +
				"import \"google/protobuf/timestamp.proto\";\n" +
				"import \"google/protobuf/duration.proto\";\n" +
				"message M { google.protobuf.Timestamp t = 1; }\n",
			want: "1a020002",
		},
		{
			name: "no syntax and an unused import",
			src:  "package p;\nimport \"google/protobuf/empty.proto\";\nmessage M {}\n",
			want: "10011a0100",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := compileWithSourceInfo(t, tt.src)
			b, err := proto.MarshalOptions{Deterministic: true}.Marshal(f.GetSourceCodeInfo())
			require.NoError(t, err)

			var got []string
			first := true
			for len(b) > 0 {
				num, typ, n := protowire.ConsumeTag(b)
				require.GreaterOrEqual(t, n, 0, "malformed SourceCodeInfo")
				b = b[n:]
				if num == bufSourceCodeInfoExtension && typ == protowire.BytesType {
					assert.True(t, first, "the extension is not SourceCodeInfo's first field")
					v, m := protowire.ConsumeBytes(b)
					require.GreaterOrEqual(t, m, 0, "malformed extension")
					got = append(got, hex.EncodeToString(v))
				}
				m := protowire.ConsumeFieldValue(num, typ, b)
				require.GreaterOrEqual(t, m, 0, "malformed SourceCodeInfo")
				b = b[m:]
				first = false
			}
			if tt.want == "" {
				assert.Empty(t, got)
				return
			}
			assert.Equal(t, []string{tt.want}, got)
		})
	}
}

// TestBufSourceCodeInfoExtensionIsNotRegistered checks that writing buf's
// extension registers nothing globally, so a program that also links buf's
// generated buf/descriptor/v1 code does not panic at startup on a second
// registration of the same file (#266).
func TestBufSourceCodeInfoExtensionIsNotRegistered(t *testing.T) {
	t.Parallel()

	compileWithSourceInfo(t, "package p;\nimport \"google/protobuf/empty.proto\";\nmessage M {}\n")
	_, err := protoregistry.GlobalFiles.FindFileByPath("buf/descriptor/v1/source_code_info_extension.proto")
	require.ErrorIs(t, err, protoregistry.NotFound)
	_, err = protoregistry.GlobalTypes.FindExtensionByNumber("google.protobuf.SourceCodeInfo", bufSourceCodeInfoExtension)
	require.ErrorIs(t, err, protoregistry.NotFound)
}
