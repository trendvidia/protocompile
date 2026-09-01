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
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/trendvidia/protocompile/internal/descsrc"
	"github.com/trendvidia/protocompile/source"
)

// requireRoundTrip is [TestRoundTrip]'s oracle applied to one hand-written
// file: compile it, render the descriptor back, recompile, and require the
// two descriptors to be equal.
//
// These cases are the ones the corpus does not reach. The corpus is real
// protoc test data, so every descriptor it produces is one this compiler
// built from valid source, and the constructs below either do not occur in
// it or occur only in the arrangement that happens to work.
func requireRoundTrip(t *testing.T, src string) {
	t.Helper()

	openerFor := func(text string) source.Opener {
		return &source.Openers{
			source.NewMap(map[string]*source.File{"t.proto": source.NewFile("t.proto", text)}),
			source.WKTs(),
		}
	}

	want, err := compile(t, openerFor(src), "t.proto")
	if err != nil {
		t.Fatalf("fixture does not compile: %v", err)
	}
	rendered, err := descsrc.Render(want)
	if err != nil {
		t.Fatalf("render refused: %v", err)
	}
	got, err := compile(t, openerFor(rendered), "t.proto")
	if err != nil {
		t.Fatalf("rendered source does not compile: %v\n\n--- rendered ---\n%s", err, rendered)
	}

	want, _ = proto.Clone(want).(*descriptorpb.FileDescriptorProto)
	got, _ = proto.Clone(got).(*descriptorpb.FileDescriptorProto)
	want.SourceCodeInfo, got.SourceCodeInfo = nil, nil
	if diff := cmp.Diff(want, got, protocmp.Transform()); diff != "" {
		t.Errorf("descriptor changed across render/recompile (-want +got):\n%s\n\n--- rendered ---\n%s",
			diff, rendered)
	}
}

// TestMapEntryNeedsAQualifiedMatch pins that a field is recognised as a map
// by its whole type name.
//
// Matching on the last dot-segment made `.Other.FooEntry` indistinguishable
// from a sibling map's `FooEntry`, so `bar` rendered as `map<int32, string>`
// — a silently different descriptor, which is the one failure mode the
// package's contract rules out.
func TestMapEntryNeedsAQualifiedMatch(t *testing.T) {
	t.Parallel()
	requireRoundTrip(t, `syntax = "proto2";
package p;
message Other { message FooEntry { optional int32 k = 1; optional string v = 2; } }
message M {
  map<int32, string> foo = 1;
  repeated Other.FooEntry bar = 2;
}
`)
}

// TestNestedMessageAfterGroupInOneof pins the schedule being flushed at
// every member of a oneof, not only the first.
//
// A group is a schedule anchor at its own field index, and a group may be a
// non-first member of a oneof. Skipping straight to the next field there
// dropped every nested message declared after it.
func TestNestedMessageAfterGroupInOneof(t *testing.T) {
	t.Parallel()
	requireRoundTrip(t, `syntax = "proto2";
message M {
  oneof o {
    int32 a = 1;
    group G = 2 { optional int32 x = 1; }
  }
  message N { optional int32 y = 1; }
}
`)
}

// TestGroupInMessageScopeExtend pins that a group declared in a
// message-scope `extend` is emitted once.
//
// Its body message was not recognised as synthesized, so it was scheduled as
// a nested message of its own and also emitted inline by its block, and
// nested_type came back with two entries named G.
func TestGroupInMessageScopeExtend(t *testing.T) {
	t.Parallel()
	requireRoundTrip(t, `syntax = "proto2";
message Foo { extensions 1 to 100; }
message M { extend Foo { optional group G = 1 { optional int32 x = 1; } } }
`)
}

// TestExtendBlocksKeepDeclarationOrder pins that each `extend` block is
// emitted where its own group body sits in message_type.
//
// Emitting every block at the first body put G2 before A, and message_type
// round-tripped as [Foo, Bar, G1, G2, A] instead of [Foo, Bar, G1, A, G2].
func TestExtendBlocksKeepDeclarationOrder(t *testing.T) {
	t.Parallel()
	requireRoundTrip(t, `syntax = "proto2";
message Foo { extensions 1 to 100; }
message Bar { extensions 1 to 100; }
extend Foo { optional group G1 = 1 { optional int32 x = 1; } }
message A { optional int32 z = 1; }
extend Bar { optional group G2 = 2 { optional int32 y = 1; } }
`)
}

// TestExtendBlocksAreNotFoldedByExtendee pins that two non-adjacent blocks
// extending the same message stay two blocks.
//
// Grouping by extendee rather than by consecutive run rewrote the extension
// list from [a, b, c] to [a, c, b].
func TestExtendBlocksAreNotFoldedByExtendee(t *testing.T) {
	t.Parallel()
	requireRoundTrip(t, `syntax = "proto2";
message Foo { extensions 1 to 100; }
message Bar { extensions 1 to 100; }
extend Foo { optional int32 a = 1; }
extend Bar { optional int32 b = 1; }
extend Foo { optional int32 c = 2; }
`)
}

// TestNestedMessageBetweenExtendBlocks pins that a nested message declared
// between two message-scope `extend` blocks stays between them.
func TestNestedMessageBetweenExtendBlocks(t *testing.T) {
	t.Parallel()
	requireRoundTrip(t, `syntax = "proto2";
message Foo { extensions 1 to 100; }
message Bar { extensions 1 to 100; }
message M {
  extend Foo { optional group G1 = 1 { optional int32 x = 1; } }
  message N { optional int32 y = 1; }
  extend Bar { optional group G2 = 2 { optional int32 z = 1; } }
}
`)
}

// TestOneofIndexOutOfRangeIsAnError pins that a descriptor no compiler would
// produce is reported, not crashed on.
//
// Render is reachable from a caller-supplied SearchResult.Proto, so its
// input is not this compiler's output and carries none of its invariants.
// Indexing oneof_decl with the field's oneof_index panicked the compiler on
// exactly the input the package exists to accept.
func TestOneofIndexOutOfRangeIsAnError(t *testing.T) {
	t.Parallel()

	_, err := descsrc.Render(&descriptorpb.FileDescriptorProto{
		Name:   proto.String("t.proto"),
		Syntax: proto.String("proto2"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("M"),
			Field: []*descriptorpb.FieldDescriptorProto{{
				Name:       proto.String("a"),
				Number:     proto.Int32(1),
				Label:      descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				Type:       descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum(),
				OneofIndex: proto.Int32(3),
			}},
		}},
	})
	if err == nil {
		t.Fatal("want an error for a field naming a oneof the message does not declare")
	}
	if !strings.Contains(err.Error(), "oneof index 3") {
		t.Errorf("error should name the offending index, got: %v", err)
	}
	// A malformed descriptor is not a fidelity boundary, and the package
	// documents the wrapping as what tells the two apart.
	if errors.Is(err, descsrc.ErrUnsupported) {
		t.Errorf("a malformed descriptor must not be reported as unsupported: %v", err)
	}
}

// TestOrphanMapEntryAtFileScopeIsRefused pins the file-scope half of a check
// that only covered nested types.
//
// A map-entry message no field claims has no `map<K, V>` spelling that would
// recreate it. Nested, that was refused; at file scope the message was
// dropped and Render returned a file with nothing in it and no error.
func TestOrphanMapEntryAtFileScopeIsRefused(t *testing.T) {
	t.Parallel()

	intField := func(name string, num int32, typ descriptorpb.FieldDescriptorProto_Type) *descriptorpb.FieldDescriptorProto {
		return &descriptorpb.FieldDescriptorProto{
			Name:   proto.String(name),
			Number: proto.Int32(num),
			Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			Type:   typ.Enum(),
		}
	}
	_, err := descsrc.Render(&descriptorpb.FileDescriptorProto{
		Name:   proto.String("t.proto"),
		Syntax: proto.String("proto2"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name:    proto.String("Orphan"),
			Options: &descriptorpb.MessageOptions{MapEntry: proto.Bool(true)},
			Field: []*descriptorpb.FieldDescriptorProto{
				intField("key", 1, descriptorpb.FieldDescriptorProto_TYPE_INT32),
				intField("value", 2, descriptorpb.FieldDescriptorProto_TYPE_STRING),
			},
		}},
	})
	if !errors.Is(err, descsrc.ErrUnsupported) {
		t.Fatalf("want an ErrUnsupported refusal, got: %v", err)
	}
	if !strings.Contains(err.Error(), "Orphan") {
		t.Errorf("error should name the message, got: %v", err)
	}
}

// TestExtendBlockBeforeAFieldRoundTrips covers a message body whose
// `extend` block is declared before a field that also synthesizes a nested
// type.
//
// Both put an entry in nested_type at their own position, so emitting every
// field before any extend block moved the block's group body after the map
// entry. That used to be refused by name — safe, but a construct valid
// source can produce. Extend blocks are now emitted among the fields at the
// position their body occupies.
func TestExtendBlockBeforeAFieldRoundTrips(t *testing.T) {
	t.Parallel()

	requireRoundTrip(t, `syntax = "proto2";
message Foo { extensions 1 to 100; }
message M {
  extend Foo { optional group G = 1 { optional int32 x = 1; } }
  map<int32, string> m = 2;
}
`)
}

// TestExtendBlocksInterleavedWithFields is the general case: blocks before,
// between and after the fields that anchor them, so a single flush point
// would put at least one of them in the wrong place.
func TestExtendBlocksInterleavedWithFields(t *testing.T) {
	t.Parallel()

	requireRoundTrip(t, `syntax = "proto2";
message Foo { extensions 1 to 100; }
message M {
  extend Foo { optional group A = 1 { optional int32 a = 1; } }
  map<int32, string> m1 = 2;
  extend Foo { optional group B = 3 { optional int32 b = 1; } }
  map<int32, string> m2 = 4;
  extend Foo { optional group C = 5 { optional int32 c = 1; } }
}
`)
}

// TestExtendBlockWithoutAGroupKeepsWorking guards the unconstrained case:
// a block declaring no group puts nothing in nested_type, so its position
// is unobservable and it may go out with the rest at the end.
func TestExtendBlockWithoutAGroupKeepsWorking(t *testing.T) {
	t.Parallel()

	requireRoundTrip(t, `syntax = "proto2";
message Foo { extensions 1 to 100; }
message M {
  extend Foo { optional int32 plain = 1; }
  map<int32, string> m = 2;
  extend Foo { optional group G = 3 { optional int32 x = 1; } }
}
`)
}
