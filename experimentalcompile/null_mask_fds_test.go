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
	"io/fs"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/trendvidia/protocompile/fdp"
	"github.com/trendvidia/protocompile/incremental"
	"github.com/trendvidia/protocompile/incremental/queries"
	"github.com/trendvidia/protocompile/ir"
	"github.com/trendvidia/protocompile/report"
	"github.com/trendvidia/protocompile/source"
)

// allPaths selects every file for [ir.Session.NullMask].
func allPaths(string) bool { return true }

// nullMaskFDS builds the descriptor set for root through [queries.FDS], the
// path `pxf build` takes, with `_null` masks on, as `pxf build` would.
func nullMaskFDS(t *testing.T, sources map[string]string, root string, options fdp.Options) (*descriptorpb.FileDescriptorSet, *report.Report) {
	t.Helper()
	return nullMaskFDSWith(t, source.WKTs(), allPaths, sources, root, options)
}

// nullMaskFDSWith is [nullMaskFDS] with the well-known types from wkts, and
// masks in the files masker selects.
func nullMaskFDSWith(t *testing.T, wkts source.Opener, masker func(string) bool, sources map[string]string, root string, options fdp.Options) (*descriptorpb.FileDescriptorSet, *report.Report) {
	t.Helper()
	files := source.NewMap(nil)
	for path, text := range sources {
		files.Add(path, text)
	}
	chain := source.Openers{files, wkts}
	results, r, err := incremental.Run(t.Context(), incremental.New(), queries.FDS{
		Opener:    &chain,
		Session:   &ir.Session{NullMask: masker},
		Workspace: source.NewWorkspace(root),
		Options:   options,
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	return results[0].Value, r
}

func diagnostics(r *report.Report, level report.Level) []string {
	var out []string
	for i := range r.Diagnostics {
		if d := &r.Diagnostics[i]; d.Level() == level {
			out = append(out, d.Message())
		}
	}
	return out
}

// A descriptor set carries field_mask.proto when a file's mask needs it,
// though the file never imported it, so the set loads on its own
// (protocompile#250).
func TestNullMask_DescriptorSetCarriesFieldMask(t *testing.T) {
	t.Parallel()
	fds, r := nullMaskFDS(t, map[string]string{
		"m.proto": `syntax = "proto3"; package m; message M { optional int32 o = 1; }`,
	}, "m.proto", fdp.Options{})
	require.Empty(t, diagnostics(r, report.Error))

	assert.Equal(t, []string{"google/protobuf/field_mask.proto", "m.proto"}, fileNames(fds))

	reg, err := protodesc.NewFiles(fds)
	require.NoError(t, err, "the set must load without the global registry")
	d, err := reg.FindDescriptorByName("m.M")
	require.NoError(t, err)
	md, ok := d.(protoreflect.MessageDescriptor)
	require.True(t, ok)
	mask := md.Fields().ByName("_null")
	require.NotNil(t, mask)
	assert.Equal(t, protoreflect.FullName("google.protobuf.FieldMask"), mask.Message().FullName())
}

// ExcludeFiles treats the mask's file like any other import.
func TestNullMask_DescriptorSetExcludeFiles(t *testing.T) {
	t.Parallel()
	var options fdp.Options
	options.Apply(fdp.ExcludeFiles(wktExcluder{}))
	fds, _ := nullMaskFDS(t, map[string]string{
		"m.proto": `syntax = "proto3"; package m; message M { optional int32 o = 1; }`,
	}, "m.proto", options)
	require.Len(t, fds.GetFile(), 1)
	assert.Equal(t, "m.proto", fds.GetFile()[0].GetName())
	assert.Contains(t, fds.GetFile()[0].GetDependency(), "google/protobuf/field_mask.proto")
}

type wktExcluder struct{}

func (wktExcluder) Exclude(f *ir.File) bool { return f.Path() == ir.NullMaskImport }

// The descriptor-set path reports what stops a message getting a mask,
// as Compile does: lowering runs the checks (protocompile#250).
func TestNullMask_DescriptorSetReportsCollisions(t *testing.T) {
	t.Parallel()
	_, r := nullMaskFDS(t, map[string]string{
		"c.proto": `syntax = "proto3"; package c; message M { optional int32 o = 1; string top = 536870911; }`,
	}, "c.proto", fdp.Options{})
	errs := diagnostics(r, report.Error)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "field `top` uses number 536870911, which protowire claims for the `_null` mask")
}

// The implicit import changes nothing the author can see: field_mask.proto's
// names are still unresolvable without an import, no unused-import warning
// appears, and a file whose messages need no mask gets no dependency.
func TestNullMask_ImplicitImportIsInvisible(t *testing.T) {
	t.Parallel()
	_, r := nullMaskFDS(t, map[string]string{
		"u.proto": `syntax = "proto3"; package u; message M { optional google.protobuf.FieldMask f = 1; }`,
	}, "u.proto", fdp.Options{})
	errs := diagnostics(r, report.Error)
	require.NotEmpty(t, errs, "google.protobuf.FieldMask must not resolve without an import")
	assert.Contains(t, errs[0], "google.protobuf.FieldMask")

	fds, r := nullMaskFDS(t, map[string]string{
		"p.proto": `syntax = "proto3"; package p; message P { int32 n = 1; }`,
	}, "p.proto", fdp.Options{})
	assert.Empty(t, diagnostics(r, report.Error))
	assert.Empty(t, diagnostics(r, report.Warning))
	assert.Equal(t, []string{"p.proto"}, fileNames(fds),
		"no message needs a mask, so the set does not carry field_mask.proto")
	assert.Empty(t, fds.GetFile()[0].GetDependency(), "no message needs a mask, so no dependency")
	_, err := protodesc.NewFiles(fds)
	require.NoError(t, err)
}

func fileNames(fds *descriptorpb.FileDescriptorSet) []string {
	names := make([]string, 0, len(fds.GetFile()))
	for _, f := range fds.GetFile() {
		names = append(names, f.GetName())
	}
	return names
}

// withoutFieldMask serves every well-known type but field_mask.proto.
type withoutFieldMask struct{}

func (*withoutFieldMask) Open(path string) (*source.File, error) {
	if path == ir.NullMaskImport {
		return nil, fs.ErrNotExist
	}
	return source.WKTs().Open(path)
}

// Without field_mask.proto, only a file with a message that needs a mask
// fails, once, at that message; it gets no mask and no dependency, so its
// descriptor stays loadable. A file with no such message is unaffected.
func TestNullMask_FieldMaskUnavailable(t *testing.T) {
	t.Parallel()
	fds, r := nullMaskFDSWith(t, &withoutFieldMask{}, allPaths, map[string]string{
		"p.proto": `syntax = "proto3"; package p; message P { int32 n = 1; }`,
	}, "p.proto", fdp.Options{})
	assert.Empty(t, r.Diagnostics, "p.P needs no mask, so it needs no field_mask.proto")
	assert.Equal(t, []string{"p.proto"}, fileNames(fds))

	fds, r = nullMaskFDSWith(t, &withoutFieldMask{}, allPaths, map[string]string{
		"q.proto": `syntax = "proto3"; package q; message Q { optional int32 n = 1; }`,
	}, "q.proto", fdp.Options{})
	require.Len(t, r.Diagnostics, 1)
	d := &r.Diagnostics[0]
	assert.Equal(t, report.Error, d.Level())
	assert.Contains(t, d.Message(), "could not import \"google/protobuf/field_mask.proto\" for the generated `_null` mask in `q.Q`")
	assert.Equal(t, "message Q { optional int32 n = 1; }", d.Primary().Text(), "the error points at the message")

	require.Equal(t, []string{"q.proto"}, fileNames(fds))
	q := fds.GetFile()[0]
	assert.Empty(t, q.GetDependency())
	assert.Empty(t, q.GetMessageType()[0].GetField()[1:], "no `_null` field without its type")
	_, err := protodesc.NewFiles(fds)
	require.NoError(t, err)
}

// A file that imports field_mask.proto itself, and cannot open it, gets
// one error, at its import, and not a second one for the mask.
func TestNullMask_DirectImportMissing(t *testing.T) {
	t.Parallel()
	_, r := nullMaskFDSWith(t, &withoutFieldMask{}, allPaths, map[string]string{
		"q.proto": `syntax = "proto3"; package q; import "google/protobuf/field_mask.proto"; message Q { optional int32 n = 1; }`,
	}, "q.proto", fdp.Options{})
	errs := diagnostics(r, report.Error)
	require.Len(t, errs, 1, "%v", errs)
	assert.Contains(t, errs[0], `imported file "google/protobuf/field_mask.proto" does not exist`)
}

// The session's masker sees canonical paths, in the query that resolves
// field_mask.proto as in lowering: a root spelled "./d.proto" is d.proto.
func TestNullMask_NonCanonicalRoot(t *testing.T) {
	t.Parallel()
	fds, r := nullMaskFDSWith(t, source.WKTs(), func(path string) bool { return path == "d.proto" }, map[string]string{
		"./d.proto": `syntax = "proto3"; package d; message D { optional int32 n = 1; }`,
	}, "./d.proto", fdp.Options{})
	require.Empty(t, r.Diagnostics)
	assert.Equal(t, []string{"google/protobuf/field_mask.proto", "d.proto"}, fileNames(fds))
	_, err := protodesc.NewFiles(fds)
	require.NoError(t, err)
}

// fdp.DescriptorSetBytes over a lowered file carries field_mask.proto as
// queries.FDS does (protocompile#250).
func TestNullMask_DescriptorSetBytes(t *testing.T) {
	t.Parallel()
	files := source.NewMap(nil)
	files.Add("m.proto", `syntax = "proto3"; package m; message M { optional int32 o = 1; }`)
	chain := source.Openers{files, source.WKTs()}
	results, r, err := incremental.Run(t.Context(), incremental.New(), queries.IR{
		Opener:  &chain,
		Session: &ir.Session{NullMask: allPaths},
		Path:    "m.proto",
	})
	require.NoError(t, err)
	require.Empty(t, r.Diagnostics)
	raw, err := fdp.DescriptorSetBytes([]*ir.File{results[0].Value})
	require.NoError(t, err)
	fds := new(descriptorpb.FileDescriptorSet)
	require.NoError(t, proto.Unmarshal(raw, fds))
	assert.Equal(t, []string{"google/protobuf/field_mask.proto", "m.proto"}, fileNames(fds))
	_, err = protodesc.NewFiles(fds)
	require.NoError(t, err)
}

// In an editions file the mask is length-prefixed even under
// `message_encoding = DELIMITED` on the file: its encoding is part of its
// wire contract (protocompile#251). The authored field in the same file
// is the control: it does become a group.
func TestNullMask_EditionsDelimitedStaysLengthPrefixed(t *testing.T) {
	t.Parallel()
	fds, r := nullMaskFDS(t, map[string]string{"e.proto": `edition = "2023"; package e;
option features.message_encoding = DELIMITED;
message Inner { int32 x = 1; }
message M { Inner i = 1; }`}, "e.proto", fdp.Options{})
	require.Empty(t, diagnostics(r, report.Error))

	// Authored options stay unknown bytes until a round trip folds them
	// (see the fdp package doc); without it DELIMITED is invisible.
	raw, err := proto.Marshal(fds)
	require.NoError(t, err)
	fds = new(descriptorpb.FileDescriptorSet)
	require.NoError(t, proto.Unmarshal(raw, fds))

	reg, err := protodesc.NewFiles(fds)
	require.NoError(t, err)
	d, err := reg.FindDescriptorByName("e.M")
	require.NoError(t, err)
	md, ok := d.(protoreflect.MessageDescriptor)
	require.True(t, ok)
	require.Equal(t, protoreflect.GroupKind, md.Fields().ByName("i").Kind(),
		"control: the authored field follows DELIMITED")
	mask := md.Fields().ByName("_null")
	require.NotNil(t, mask)
	assert.Equal(t, protoreflect.MessageKind, mask.Kind(), "the mask must not be a group")
}
