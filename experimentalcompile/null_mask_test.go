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

package experimentalcompile_test

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/trendvidia/protocompile"
	"github.com/trendvidia/protocompile/linker"
	"github.com/trendvidia/protocompile/protoutil"
	"github.com/trendvidia/protocompile/reporter"
)

// nullMaskCompile compiles the named files from sources with `_null`
// masks enabled for every path, or disabled when masks is false. It
// returns the files, the warnings and the errors reported.
func nullMaskCompile(t *testing.T, masks bool, sources map[string]string, roots ...string) (linker.Files, []string, []string) {
	t.Helper()
	var warns, errs []string
	c := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(protocompile.ResolverFunc(func(path string) (protocompile.SearchResult, error) {
			if src, ok := sources[path]; ok {
				return protocompile.SearchResult{Source: io.NopCloser(strings.NewReader(src))}, nil
			}
			return protocompile.SearchResult{}, os.ErrNotExist
		})),
		Reporter: reporter.NewReporter(
			func(err reporter.ErrorWithPos) error { errs = append(errs, err.Error()); return nil },
			func(err reporter.ErrorWithPos) { warns = append(warns, err.Error()) },
		),
	}
	if masks {
		c.NullMask = func(string) bool { return true }
	}
	files, err := c.Compile(t.Context(), roots...)
	if len(errs) == 0 {
		require.NoError(t, err)
	} else {
		require.ErrorIs(t, err, reporter.ErrInvalidSource)
	}
	return files, warns, errs
}

func findMessage(msgs protoreflect.MessageDescriptors, name protoreflect.FullName) (protoreflect.MessageDescriptor, bool) {
	for i := range msgs.Len() {
		md := msgs.Get(i)
		if md.FullName() == name {
			return md, true
		}
		if found, ok := findMessage(md.Messages(), name); ok {
			return found, true
		}
	}
	return nil, false
}

const nullMaskProto3 = `
syntax = "proto3";
package m;

message Inner { int32 x = 1; }

message Optional { optional int32 o = 1; }
message Message { Inner i = 1; }
message Oneof { oneof k { string ks = 1; } }
message Plain { int32 n = 1; string s = 2; }
message Lists { repeated int32 l = 1; map<string, int32> by = 2; }
message Nested {
  message Deep { optional string d = 1; }
  int32 n = 1;
}
`

// A message gets the mask exactly when it has a field able to hold a PXF
// null: a message field, a proto3 `optional` scalar, a oneof member
// (protowire#339). The field is a singular google.protobuf.FieldMask
// named `_null`, numbered 536870911, with the JSON name protoc would
// derive, and the file depends on field_mask.proto.
func TestNullMask_AddsToMessagesThatMayHoldNull(t *testing.T) {
	t.Parallel()
	files, warns, errs := nullMaskCompile(t, true, map[string]string{"m.proto": nullMaskProto3}, "m.proto")
	require.Empty(t, errs)
	require.Empty(t, warns)
	file := files[0]

	for _, msg := range []string{"Optional", "Message", "Oneof", "Nested.Deep", "Inner"} {
		md, ok := findMessage(file.Messages(), protoreflect.FullName("m."+msg))
		require.True(t, ok, msg)
		f := md.Fields().ByName("_null")
		if msg == "Inner" {
			assert.Nil(t, f, "Inner has only a plain scalar")
			continue
		}
		require.NotNil(t, f, "%s should get a mask", msg)
		assert.Equal(t, protoreflect.FieldNumber(536870911), f.Number(), msg)
		assert.Equal(t, protoreflect.MessageKind, f.Kind(), msg)
		assert.Equal(t, protoreflect.FullName("google.protobuf.FieldMask"), f.Message().FullName(), msg)
		assert.Equal(t, protoreflect.Optional, f.Cardinality(), msg)
		assert.Equal(t, "Null", f.JSONName(), msg)
		assert.Equal(t, md.Fields().Len()-1, f.Index(), "%s: the mask is the last field", msg)
	}
	for _, msg := range []string{"Plain", "Lists", "Lists.ByEntry", "Nested"} {
		md, ok := findMessage(file.Messages(), protoreflect.FullName("m."+msg))
		require.True(t, ok, msg)
		assert.Nil(t, md.Fields().ByName("_null"), "%s has no field that may hold null", msg)
	}

	fdp := protoutil.ProtoFromFileDescriptor(file)
	assert.Contains(t, fdp.GetDependency(), "google/protobuf/field_mask.proto")
}

// Masks are off unless the caller asks for them.
func TestNullMask_OffByDefault(t *testing.T) {
	t.Parallel()
	files, _, errs := nullMaskCompile(t, false, map[string]string{"m.proto": nullMaskProto3}, "m.proto")
	require.Empty(t, errs)
	md, _ := findMessage(files[0].Messages(), "m.Optional")
	assert.Nil(t, md.Fields().ByName("_null"))
	assert.NotContains(t, protoutil.ProtoFromFileDescriptor(files[0]).GetDependency(), "google/protobuf/field_mask.proto")
}

// A proto2 singular field has presence, so it may hold null.
func TestNullMask_Proto2SingularField(t *testing.T) {
	t.Parallel()
	files, _, errs := nullMaskCompile(t, true, map[string]string{"p.proto": `
syntax = "proto2";
package p;
message M { optional int32 a = 1; repeated int32 r = 2; }
message R { repeated int32 r = 1; }
`}, "p.proto")
	require.Empty(t, errs)
	m, _ := findMessage(files[0].Messages(), "p.M")
	assert.NotNil(t, m.Fields().ByName("_null"))
	r, _ := findMessage(files[0].Messages(), "p.R")
	assert.Nil(t, r.Fields().ByName("_null"))
}

// An imported file compiled from source gets masks too, so a message has
// the same descriptor wherever it is reached from.
func TestNullMask_ImportedFile(t *testing.T) {
	t.Parallel()
	files, _, errs := nullMaskCompile(t, true, map[string]string{
		"a.proto": `syntax = "proto3"; package a; import "b.proto"; message A { b.B b = 1; }`,
		"b.proto": `syntax = "proto3"; package b; message B { optional int32 o = 1; }`,
	}, "a.proto")
	require.Empty(t, errs)
	b := files[0].Imports().Get(0).FileDescriptor
	require.Equal(t, "b.proto", b.Path())
	assert.NotNil(t, b.Messages().ByName("B").Fields().ByName("_null"))
}

// A hand-declared `_null` wins: its number stands and nothing is added.
func TestNullMask_HandDeclaredWins(t *testing.T) {
	t.Parallel()
	files, warns, errs := nullMaskCompile(t, true, map[string]string{"h.proto": `
syntax = "proto3";
package h;
import "google/protobuf/field_mask.proto";
message M {
  optional int32 o = 1;
  google.protobuf.FieldMask _null = 15;
}
`}, "h.proto")
	require.Empty(t, errs)
	require.Empty(t, warns)
	m := files[0].Messages().ByName("M")
	require.Equal(t, 2, m.Fields().Len())
	assert.Equal(t, protoreflect.FieldNumber(15), m.Fields().ByName("_null").Number())
}

// A hand-declared `_null` that is not a singular FieldMask is an error.
func TestNullMask_HandDeclaredWrongType(t *testing.T) {
	t.Parallel()
	for name, decl := range map[string]string{
		"string":   "string _null = 15;",
		"repeated": `repeated google.protobuf.FieldMask _null = 15;`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, _, errs := nullMaskCompile(t, true, map[string]string{"h.proto": `
syntax = "proto3";
package h;
import "google/protobuf/field_mask.proto";
message M { optional int32 o = 1; ` + decl + ` }
`}, "h.proto")
			require.Len(t, errs, 1)
			assert.Contains(t, errs[0], "`_null` in `h.M` must be a singular `google.protobuf.FieldMask`")
		})
	}
}

// An authored field that takes the mask's number or JSON name is an
// error naming that field.
func TestNullMask_AuthoredCollision(t *testing.T) {
	t.Parallel()
	_, _, errs := nullMaskCompile(t, true, map[string]string{"c.proto": `
syntax = "proto3";
package c;
message M { optional int32 o = 1; string top = 536870911; }
`}, "c.proto")
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "field `top` uses number 536870911, which protowire claims for the `_null` mask")

	_, _, errs = nullMaskCompile(t, true, map[string]string{"j.proto": `
syntax = "proto3";
package j;
message M { optional int32 o = 1; string Null = 2; }
`}, "j.proto")
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "field `Null` has the JSON name `Null`, which the generated `_null` mask takes")
}

// A range or name the author reserved over the mask leaves the message
// without one, with a warning: an error would refuse ordinary proto2
// schemas such as `extensions 1000 to max`.
func TestNullMask_ReservedOverMaskWarns(t *testing.T) {
	t.Parallel()
	cases := map[string]struct{ decl, want string }{
		"extensions":     {"extensions 1000 to max;", "`r.M` gets no `_null` mask: an extension range covers number 536870911"},
		"reserved range": {"reserved 1000 to max;", "`r.M` gets no `_null` mask: a reserved range covers number 536870911"},
		"reserved name":  {`reserved "_null";`, "`r.M` gets no `_null` mask: the name `_null` is reserved"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			files, warns, errs := nullMaskCompile(t, true, map[string]string{"r.proto": `
syntax = "proto2";
package r;
message M { optional int32 a = 1; ` + c.decl + ` }
`}, "r.proto")
			require.Empty(t, errs)
			require.Len(t, warns, 1)
			assert.Contains(t, warns[0], c.want)
			assert.Nil(t, files[0].Messages().ByName("M").Fields().ByName("_null"))
		})
	}
}

// protobuf's own files never get a mask, even with every path selected:
// descriptor.proto's proto2 option messages all have fields with
// presence.
func TestNullMask_StandardImportsUntouched(t *testing.T) {
	t.Parallel()
	files, warns, errs := nullMaskCompile(t, true, map[string]string{"o.proto": `
syntax = "proto3";
package o;
import "google/protobuf/descriptor.proto";
import "google/protobuf/wrappers.proto";
extend google.protobuf.FieldOptions { optional string tag = 50000; }
message M { google.protobuf.StringValue s = 1; }
`}, "o.proto")
	require.Empty(t, errs)
	require.Empty(t, warns, "descriptor.proto's `extensions 1000 to max` must not warn")
	for i := range files[0].Imports().Len() {
		imp := files[0].Imports().Get(i).FileDescriptor
		require.True(t, strings.HasPrefix(imp.Path(), "google/protobuf/"), imp.Path())
		msgs := imp.Messages()
		for j := range msgs.Len() {
			assert.Nil(t, msgs.Get(j).Fields().ByName("_null"), "%s", msgs.Get(j).FullName())
		}
	}
	assert.NotNil(t, files[0].Messages().ByName("M").Fields().ByName("_null"))
}

// The caller's filter decides per file.
func TestNullMask_FilterByPath(t *testing.T) {
	t.Parallel()
	c := protocompile.Compiler{
		Resolver: protocompile.ResolverFunc(func(path string) (protocompile.SearchResult, error) {
			src := map[string]string{
				"keep.proto": `syntax = "proto3"; package k; import "skip.proto"; message K { optional int32 o = 1; s.S s = 2; }`,
				"skip.proto": `syntax = "proto3"; package s; message S { optional int32 o = 1; }`,
			}[path]
			if src == "" {
				return protocompile.SearchResult{}, os.ErrNotExist
			}
			return protocompile.SearchResult{Source: io.NopCloser(strings.NewReader(src))}, nil
		}),
		NullMask: func(path string) bool { return path != "skip.proto" },
	}
	files, err := c.Compile(t.Context(), "keep.proto")
	require.NoError(t, err)
	assert.NotNil(t, files[0].Messages().ByName("K").Fields().ByName("_null"))
	s := files[0].Imports().Get(0).FileDescriptor
	assert.Nil(t, s.Messages().ByName("S").Fields().ByName("_null"))
}
