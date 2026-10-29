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

// Package bufext builds buf's SourceCodeInfo extension
// (buf.descriptor.v1.buf_source_code_info_extension) without importing buf's
// generated code, which lives in modules hosted on buf.build (#266).
package bufext

import (
	"sync"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// SourceCodeInfoExtension returns buf's
// buf.descriptor.v1.buf_source_code_info_extension (field 536000000 of
// SourceCodeInfo, a SourceCodeInfoExtension message), built from a
// descriptor written here rather than imported from buf's generated code.
// Resolve with it to read the extension back as fields (#266).
//
// The generated code lives in modules hosted on buf.build, which every
// program linking fdp then downloaded at build time. This type is built
// with dynamicpb and never registered, so a program that also links buf's
// SDK does not register buf/descriptor/v1 twice.
//
// It is an extension type rather than unknown-field bytes because Go marshals
// extension fields before a message's known fields and unknown fields after
// them: bytes would move the extension from the start of SourceCodeInfo to
// its end. TestBufSourceCodeInfoExtensionBytes pins the bytes and position
// the generated code produced (see fdp).
var SourceCodeInfoExtension = sync.OnceValue(func() protoreflect.ExtensionType {
	file := &descriptorpb.FileDescriptorProto{
		Name:       proto.String("buf/descriptor/v1/source_code_info_extension.proto"),
		Package:    proto.String("buf.descriptor.v1"),
		Dependency: []string{"google/protobuf/descriptor.proto"},
		// proto2: protodesc rejects a proto3 file that extends a message
		// other than an options message. The encoding matches buf's
		// proto3 file because only true and non-empty values are set, and
		// unused_dependency is packed explicitly.
		Syntax: proto.String("proto2"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("SourceCodeInfoExtension"),
			Field: []*descriptorpb.FieldDescriptorProto{
				{
					Name:     proto.String("is_syntax_unspecified"),
					JsonName: proto.String("isSyntaxUnspecified"),
					Number:   proto.Int32(2),
					Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
					Type:     descriptorpb.FieldDescriptorProto_TYPE_BOOL.Enum(),
				},
				{
					Name:     proto.String("unused_dependency"),
					JsonName: proto.String("unusedDependency"),
					Number:   proto.Int32(3),
					Label:    descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(),
					Type:     descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum(),
					Options:  &descriptorpb.FieldOptions{Packed: proto.Bool(true)},
				},
			},
		}},
		Extension: []*descriptorpb.FieldDescriptorProto{{
			Name:     proto.String("buf_source_code_info_extension"),
			JsonName: proto.String("bufSourceCodeInfoExtension"),
			Number:   proto.Int32(536000000),
			Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
			TypeName: proto.String(".buf.descriptor.v1.SourceCodeInfoExtension"),
			Extendee: proto.String(".google.protobuf.SourceCodeInfo"),
		}},
	}
	fd, err := protodesc.NewFile(file, protoregistry.GlobalFiles)
	if err != nil {
		panic(err) // A fixed descriptor; only a bug here can make it invalid.
	}
	return dynamicpb.NewExtensionType(fd.Extensions().Get(0))
})

// Set sets buf's SourceCodeInfoExtension on info, or leaves it
// unset when both of its fields are empty.
//
// The extension's fields are encoded by hand, in field-number order, and
// carried as the dynamic message's unknown bytes: dynamicpb does not marshal
// set fields in a fixed order, and buf's generated code always wrote
// is_syntax_unspecified (2) before unused_dependency (3).
func Set(info *descriptorpb.SourceCodeInfo, syntaxUnspecified bool, unusedDependency []int32) {
	var b []byte
	if syntaxUnspecified {
		b = protowire.AppendTag(b, 2, protowire.VarintType)
		b = protowire.AppendVarint(b, protowire.EncodeBool(true))
	}
	if len(unusedDependency) > 0 {
		var packed []byte
		for _, i := range unusedDependency {
			packed = protowire.AppendVarint(packed, uint64(i))
		}
		b = protowire.AppendTag(b, 3, protowire.BytesType)
		b = protowire.AppendBytes(b, packed)
	}
	if len(b) == 0 {
		return
	}

	xt := SourceCodeInfoExtension()
	ext := dynamicpb.NewMessage(xt.TypeDescriptor().Message())
	ext.SetUnknown(b)
	info.ProtoReflect().Set(xt.TypeDescriptor(), protoreflect.ValueOfMessage(ext))
}
