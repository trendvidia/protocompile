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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/trendvidia/protocompile/incremental"
	"github.com/trendvidia/protocompile/incremental/queries"
	pwsv1 "github.com/trendvidia/protocompile/internal/gen/protowire/schema/v1"
	"github.com/trendvidia/protocompile/ir"
	"github.com/trendvidia/protocompile/source"
)

// TestTypeAliasFieldScalarBase covers the simplest substitution
// case: `type Email = string;` followed by `Email email = 1;`. The
// resolved field must have scalar string type at the FDP level.
func TestTypeAliasFieldScalarBase(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

type Email = string;

message User {
  Email email = 1;
}
`
	f := compileForFDPTest(t, src)
	require.Len(t, f.GetMessageType(), 1)
	require.Len(t, f.GetMessageType()[0].GetField(), 1)
	field := f.GetMessageType()[0].GetField()[0]
	assert.Equal(t, "email", field.GetName())
	assert.Equal(t, descriptorpb.FieldDescriptorProto_TYPE_STRING, field.GetType())
}

// TestTypeAliasFieldMessageBase covers an alias whose base is a
// user-defined message: the field should resolve as a TYPE_MESSAGE
// referencing the original FQN.
func TestTypeAliasFieldMessageBase(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

message Address {}
type Contact = test.Address;

message User {
  Contact contact = 1;
}
`
	f := compileForFDPTest(t, src)
	var user *descriptorpb.DescriptorProto
	for _, m := range f.GetMessageType() {
		if m.GetName() == "User" {
			user = m
		}
	}
	require.NotNil(t, user)
	require.Len(t, user.GetField(), 1)
	field := user.GetField()[0]
	assert.Equal(t, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE, field.GetType())
	assert.Equal(t, ".test.Address", field.GetTypeName())
}

// TestTypeAliasFieldChain covers a multi-link alias chain
// (`type A = B; type B = string;`) — both links must unwrap and
// produce a scalar string at the field site.
func TestTypeAliasFieldChain(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

type B = string;
type A = test.B;

message M {
  A a = 1;
}
`
	f := compileForFDPTest(t, src)
	require.Len(t, f.GetMessageType(), 1)
	require.Len(t, f.GetMessageType()[0].GetField(), 1)
	field := f.GetMessageType()[0].GetField()[0]
	assert.Equal(t, descriptorpb.FieldDescriptorProto_TYPE_STRING, field.GetType())
}

// TestTypeAliasFieldPropagatesAnnotations verifies the RFC-001 §5
// macro-expansion semantics: trailing annotations declared on the
// alias must show up on the field's annotation list alongside any
// field-site annotations. Order: alias-side first, then field-site.
func TestTypeAliasFieldPropagatesAnnotations(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

annotation validate(rule: string);
annotation doc(text: string);

type Email = string @validate("matches(this, '^[^@]+@[^@]+$')");

message User {
  @doc("primary contact")
  Email email = 1;
}
`
	f := compileForFDPTest(t, src)
	require.Len(t, f.GetMessageType(), 1)
	require.Len(t, f.GetMessageType()[0].GetField(), 1)
	field := f.GetMessageType()[0].GetField()[0]
	require.NotNil(t, field.Options, "field should have Options with annotation list")

	list, ok := proto.GetExtension(field.Options, pwsv1.E_FieldAnnotations).(*pwsv1.AnnotationList)
	require.True(t, ok)
	require.NotNil(t, list)
	require.Len(t, list.Entries, 2,
		"both the alias-level @validate and the field-level @doc should be present")

	// Alias-side annotation must come first, followed by field-site.
	assert.Equal(t, "test.validate", list.Entries[0].Name)
	require.Len(t, list.Entries[0].Args, 1)
	assert.Equal(t, "matches(this, '^[^@]+@[^@]+$')", list.Entries[0].Args[0].GetStringValue())

	assert.Equal(t, "test.doc", list.Entries[1].Name)
	require.Len(t, list.Entries[1].Args, 1)
	assert.Equal(t, "primary contact", list.Entries[1].Args[0].GetStringValue())
}

// TestTypeAliasFieldCycleDiagnostic verifies a cyclic alias chain
// (A → B → A) emits a diagnostic and the field's type ends up
// unresolved rather than infinite-looping.
func TestTypeAliasFieldCycleDiagnostic(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

type A = test.B;
type B = test.A;

message M {
  A a = 1;
}
`
	opener := source.NewMap(map[string]*source.File{
		"x.proto": source.NewFile("x.proto", src),
	})
	allOpeners := &source.Openers{opener, source.WKTs()}

	exec := incremental.New()
	sess := new(ir.Session)
	results, rep, err := incremental.Run(t.Context(), exec, queries.IR{
		Opener:  allOpeners,
		Session: sess,
		Path:    "x.proto",
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.NotNil(t, results[0].Value)
	require.NotNil(t, rep)

	var foundCycle bool
	for _, d := range rep.Diagnostics {
		if strings.Contains(d.Message(), "is cyclic") {
			foundCycle = true
			break
		}
	}
	assert.True(t, foundCycle, "expected a `type alias ... is cyclic` diagnostic; got %d diagnostics", len(rep.Diagnostics))
}

// TestTypeAliasFieldChainPropagatesAnnotations verifies annotation
// accumulation across the full chain: every link contributes.
func TestTypeAliasFieldChainPropagatesAnnotations(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

annotation a;
annotation b;

type B = string @b;
type A = test.B @a;

message M {
  A x = 1;
}
`
	f := compileForFDPTest(t, src)
	require.Len(t, f.GetMessageType(), 1)
	field := f.GetMessageType()[0].GetField()[0]
	require.NotNil(t, field.Options)
	list, ok := proto.GetExtension(field.Options, pwsv1.E_FieldAnnotations).(*pwsv1.AnnotationList)
	require.True(t, ok)
	require.Len(t, list.Entries, 2)
	names := []string{list.Entries[0].Name, list.Entries[1].Name}
	assert.ElementsMatch(t, []string{"test.a", "test.b"}, names,
		"both alias-chain annotations should be propagated to the field")
}
