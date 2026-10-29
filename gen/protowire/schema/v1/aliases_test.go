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

package schemav1_test

import (
	"testing"

	protos "github.com/trendvidia/protos/protowire/schema/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	schemav1 "github.com/trendvidia/protocompile/gen/protowire/schema/v1"
)

// This binary links gen/protowire/schema/v1 and
// github.com/trendvidia/protos/protowire/schema/v1, which used to register
// descriptor.proto and report.proto twice and panic at startup. Now the
// aliases are protos' types and extensions.
func TestAliasesAreProtosTypes(t *testing.T) {
	var _ *protos.Report = new(schemav1.Report)

	mt, err := protoregistry.GlobalTypes.FindMessageByName("protowire.schema.v1.Report")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := mt.New().Interface().(*schemav1.Report); !ok {
		t.Errorf("the registered Report is %T, not *schemav1.Report", mt.New().Interface())
	}

	// An extension set through the alias reads back through protos.
	opts := &descriptorpb.FieldOptions{}
	proto.SetExtension(opts, schemav1.E_FieldAnnotations, &schemav1.AnnotationList{
		Entries: []*schemav1.Annotation{{Name: "protowire.schema.v1.required"}},
	})
	got, ok := proto.GetExtension(opts, protos.E_FieldAnnotations).(*protos.AnnotationList)
	if !ok || len(got.GetEntries()) != 1 || got.GetEntries()[0].GetName() != "protowire.schema.v1.required" {
		t.Errorf("read back through protos: %v", got)
	}
}
