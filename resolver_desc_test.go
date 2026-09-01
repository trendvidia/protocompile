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

package protocompile_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/trendvidia/protocompile"
)

const descUserSrc = `syntax = "proto3";
import "common.proto";
message U { common.Shared s = 1; }
`

func commonFileDescriptorProto() *descriptorpb.FileDescriptorProto {
	return &descriptorpb.FileDescriptorProto{
		Name:    proto.String("common.proto"),
		Package: proto.String("common"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("Shared"),
			Field: []*descriptorpb.FieldDescriptorProto{{
				Name:     proto.String("id"),
				Number:   proto.Int32(1),
				Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
				JsonName: proto.String("id"),
			}},
		}},
	}
}

// TestSearchResultDescIsHonoured covers the resolver shape issue #148 was
// filed about: resolving an import by handing back an already-linked
// descriptor. Before the fix this returned not-found, and the compile
// failed as `user.proto:2:1: imported file does not exist` — an error that
// blamed the importing file and named neither the resolver nor the field.
func TestSearchResultDescIsHonoured(t *testing.T) {
	t.Parallel()

	fd, err := protodesc.NewFile(commonFileDescriptorProto(), nil)
	require.NoError(t, err)

	res := protocompile.ResolverFunc(func(path string) (protocompile.SearchResult, error) {
		switch path {
		case "user.proto":
			return protocompile.SearchResult{Source: strings.NewReader(descUserSrc)}, nil
		case "common.proto":
			return protocompile.SearchResult{Desc: fd}, nil
		}
		return protocompile.SearchResult{}, nil
	})

	files, err := (&protocompile.Compiler{Resolver: res}).Compile(context.Background(), "user.proto")
	require.NoError(t, err)
	require.Len(t, files, 1)

	// The import resolved, and the field's type came from the descriptor.
	msg := files[0].Messages().Get(0)
	assert.Equal(t, "U", string(msg.Name()))
	assert.Equal(t, "common.Shared", string(msg.Fields().Get(0).Message().FullName()))
}

// TestSearchResultProtoIsHonoured is the same for the Proto field.
func TestSearchResultProtoIsHonoured(t *testing.T) {
	t.Parallel()

	res := protocompile.ResolverFunc(func(path string) (protocompile.SearchResult, error) {
		switch path {
		case "user.proto":
			return protocompile.SearchResult{Source: strings.NewReader(descUserSrc)}, nil
		case "common.proto":
			return protocompile.SearchResult{Proto: commonFileDescriptorProto()}, nil
		}
		return protocompile.SearchResult{}, nil
	})

	files, err := (&protocompile.Compiler{Resolver: res}).Compile(context.Background(), "user.proto")
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, "common.Shared",
		string(files[0].Messages().Get(0).Fields().Get(0).Message().FullName()))
}

// TestSearchResultDescUnrenderableIsLoud is the property #148 actually
// asked for: setting Desc and getting nothing back must be impossible to do
// quietly. A descriptor the renderer cannot express produces an error that
// names the path and the reason, rather than a not-found that blames the
// importing file.
func TestSearchResultDescUnrenderableIsLoud(t *testing.T) {
	t.Parallel()

	// File options carrying an extension that is not linked into this
	// binary. There is no name to write for it, so the file cannot be
	// rendered faithfully — the documented fidelity boundary.
	broken := commonFileDescriptorProto()
	broken.Options = &descriptorpb.FileOptions{}
	broken.Options.ProtoReflect().SetUnknown(
		protowire.AppendString(protowire.AppendTag(nil, 60123, protowire.BytesType), "x"))

	res := protocompile.ResolverFunc(func(path string) (protocompile.SearchResult, error) {
		switch path {
		case "user.proto":
			return protocompile.SearchResult{Source: strings.NewReader(descUserSrc)}, nil
		case "common.proto":
			return protocompile.SearchResult{Proto: broken}, nil
		}
		return protocompile.SearchResult{}, nil
	})

	_, err := (&protocompile.Compiler{Resolver: res}).Compile(context.Background(), "user.proto")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "common.proto", "the error must name the file the resolver answered for")
	assert.NotContains(t, err.Error(), "imported file does not exist",
		"a descriptor the resolver did supply must not be reported as missing")
}

// TestEmptySearchResultIsStillNotFound guards the other side: a wholly zero
// SearchResult remains the way a resolver says not-found.
func TestEmptySearchResultIsStillNotFound(t *testing.T) {
	t.Parallel()

	res := protocompile.ResolverFunc(func(path string) (protocompile.SearchResult, error) {
		if path == "user.proto" {
			return protocompile.SearchResult{Source: strings.NewReader(descUserSrc)}, nil
		}
		return protocompile.SearchResult{}, nil
	})

	_, err := (&protocompile.Compiler{Resolver: res}).Compile(context.Background(), "user.proto")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "imported file does not exist")
}

// TestWithStandardImportsUsesDesc covers this package's own use of the field.
//
// [protocompile.WithStandardImports] answers a standard import by returning
// SearchResult{Desc: ...}. While Desc was inert that made the function a
// no-op wherever it was actually needed — it "supplied" the file and the
// compile then reported it missing.
func TestWithStandardImportsUsesDesc(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
import "google/protobuf/descriptor.proto";
message M {
  google.protobuf.FileDescriptorProto fdp = 1;
}
`
	// The inner resolver knows only the one file and errors on everything
	// else, so descriptor.proto can only come from WithStandardImports.
	inner := protocompile.ResolverFunc(func(path string) (protocompile.SearchResult, error) {
		if path == "x.proto" {
			return protocompile.SearchResult{Source: strings.NewReader(src)}, nil
		}
		return protocompile.SearchResult{}, protoregistry.NotFound
	})

	files, err := (&protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(inner),
	}).Compile(context.Background(), "x.proto")
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, "google.protobuf.FileDescriptorProto",
		string(files[0].Messages().Get(0).Fields().Get(0).Message().FullName()))
}
