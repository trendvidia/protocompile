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

package pxf_test

import (
	"testing"

	protos "github.com/trendvidia/protos/pxf"
	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/trendvidia/protocompile/gen/pxf"
)

// This binary links gen/pxf and github.com/trendvidia/protos/pxf. When
// gen/pxf held its own generated code, both registered pxf/bignum.proto and
// the program panicked before any test ran. Now there is one type, and the
// registry hands it out under either name.
func TestAliasesAreProtosTypes(t *testing.T) {
	var _ *protos.BigInt = new(pxf.BigInt)

	mt, err := protoregistry.GlobalTypes.FindMessageByName("pxf.BigInt")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := mt.New().Interface().(*pxf.BigInt); !ok {
		t.Errorf("the registered pxf.BigInt is %T, not *pxf.BigInt", mt.New().Interface())
	}
	if pxf.File_pxf_bignum_proto != protos.File_pxf_bignum_proto || pxf.File_pxf_bignum_proto == nil {
		t.Errorf("File_pxf_bignum_proto = %v, want protos' %v", pxf.File_pxf_bignum_proto, protos.File_pxf_bignum_proto)
	}
}
