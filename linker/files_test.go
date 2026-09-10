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

package linker_test

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/trendvidia/protocompile"
	"github.com/trendvidia/protocompile/linker"
)

// visibilitySources is an import graph with one edge of each kind out of
// the root's direct import:
//
//	root.proto ─imports─▶ direct.proto ─import public─▶ pub.proto ─import public─▶ deep.proto
//	                                   └─imports───────▶ private.proto ─import public─▶ privpub.proto
//
// From root.proto, everything on the top row is visible and nothing on
// the bottom row is: a non-public import is visible only to the file that
// imports it, and so is anything it re-exports.
var visibilitySources = map[string]string{
	"root.proto": `syntax = "proto3"; package r;
import "direct.proto";
message Root { d.Direct direct = 1; }`,
	"direct.proto": `syntax = "proto3"; package d;
import public "pub.proto";
import "private.proto";
message Direct { pub.Pub pub = 1; priv.Priv priv = 2; }`,
	"pub.proto": `syntax = "proto3"; package pub;
import public "deep.proto";
import "google/protobuf/descriptor.proto";
message Pub { deep.Deep deep = 1; }
extend google.protobuf.MessageOptions { string pub_ext = 51235; }`,
	"deep.proto": `syntax = "proto3"; package deep;
message Deep {}`,
	"private.proto": `syntax = "proto3"; package priv;
import public "privpub.proto";
import "google/protobuf/descriptor.proto";
message Priv { privpub.PrivPub p = 1; }
extend google.protobuf.MessageOptions { string priv_ext = 51236; }`,
	"privpub.proto": `syntax = "proto3"; package privpub;
message PrivPub {}`,
}

// TestResolverFromFile pins the visibility rule issue #222 asked for: the
// resolver over one file answers for the file itself, its direct imports,
// and their transitive public imports, and for nothing behind a
// non-public edge.
func TestResolverFromFile(t *testing.T) {
	t.Parallel()

	files := compileSources(t, visibilitySources, "root.proto")
	r := linker.ResolverFromFile(files[0])

	t.Run("FindDescriptorByName", func(t *testing.T) {
		t.Parallel()
		for _, name := range []protoreflect.FullName{"r.Root", "d.Direct", "pub.Pub", "pub.pub_ext", "deep.Deep"} {
			d, err := r.FindDescriptorByName(name)
			require.NoError(t, err, "%s is visible from root.proto", name)
			assert.Equal(t, name, d.FullName())
		}
		for _, name := range []protoreflect.FullName{"priv.Priv", "priv.priv_ext", "privpub.PrivPub", "nope.Nope"} {
			_, err := r.FindDescriptorByName(name)
			assert.ErrorIs(t, err, protoregistry.NotFound, "%s is not visible from root.proto", name)
		}
	})

	t.Run("FindFileByPath", func(t *testing.T) {
		t.Parallel()
		for _, path := range []string{"root.proto", "direct.proto", "pub.proto", "deep.proto"} {
			fd, err := r.FindFileByPath(path)
			require.NoError(t, err, "%s is visible from root.proto", path)
			assert.Equal(t, path, fd.Path())
		}
		for _, path := range []string{"private.proto", "privpub.proto", "nope.proto"} {
			_, err := r.FindFileByPath(path)
			assert.ErrorIs(t, err, protoregistry.NotFound, "%s is not visible from root.proto", path)
		}
	})

	t.Run("FindMessageByName", func(t *testing.T) {
		t.Parallel()
		mt, err := r.FindMessageByName("pub.Pub")
		require.NoError(t, err)
		assert.Equal(t, protoreflect.FullName("pub.Pub"), mt.Descriptor().FullName())

		_, err = r.FindMessageByName("privpub.PrivPub")
		assert.ErrorIs(t, err, protoregistry.NotFound)

		// A visible name of the wrong kind is an error, not a miss.
		_, err = r.FindMessageByName("pub.pub_ext")
		require.Error(t, err)
		assert.NotErrorIs(t, err, protoregistry.NotFound)
		assert.Contains(t, err.Error(), "not a message")
	})

	t.Run("FindMessageByURL", func(t *testing.T) {
		t.Parallel()
		mt, err := r.FindMessageByURL("type.googleapis.com/deep.Deep")
		require.NoError(t, err)
		assert.Equal(t, protoreflect.FullName("deep.Deep"), mt.Descriptor().FullName())
	})

	t.Run("FindExtensionByName", func(t *testing.T) {
		t.Parallel()
		xt, err := r.FindExtensionByName("pub.pub_ext")
		require.NoError(t, err)
		assert.Equal(t, protoreflect.FieldNumber(51235), xt.TypeDescriptor().Number())

		_, err = r.FindExtensionByName("priv.priv_ext")
		assert.ErrorIs(t, err, protoregistry.NotFound)

		_, err = r.FindExtensionByName("pub.Pub")
		require.Error(t, err)
		assert.NotErrorIs(t, err, protoregistry.NotFound)
		assert.Contains(t, err.Error(), "not an extension")
	})

	t.Run("FindExtensionByNumber", func(t *testing.T) {
		t.Parallel()
		xt, err := r.FindExtensionByNumber("google.protobuf.MessageOptions", 51235)
		require.NoError(t, err)
		assert.Equal(t, protoreflect.FullName("pub.pub_ext"), xt.TypeDescriptor().FullName())

		_, err = r.FindExtensionByNumber("google.protobuf.MessageOptions", 51236)
		assert.ErrorIs(t, err, protoregistry.NotFound)
	})
}

// TestResolverFromFileVersusFilesAsResolver pins the difference that made
// #222 a gap: Files.AsResolver answers only for the files listed, so a
// consumer holding one compiled root cannot reach a message its import
// declares through it.
func TestResolverFromFileVersusFilesAsResolver(t *testing.T) {
	t.Parallel()

	files := compileSources(t, visibilitySources, "root.proto")

	_, err := files.AsResolver().FindDescriptorByName("d.Direct")
	assert.ErrorIs(t, err, protoregistry.NotFound, "AsResolver sees only the listed files")

	d, err := linker.ResolverFromFile(files[0]).FindDescriptorByName("d.Direct")
	require.NoError(t, err)
	assert.Equal(t, protoreflect.FullName("d.Direct"), d.FullName())
}

func compileSources(t *testing.T, sources map[string]string, roots ...string) linker.Files {
	t.Helper()
	c := protocompile.Compiler{
		Resolver: protocompile.ResolverFunc(func(path string) (protocompile.SearchResult, error) {
			src, ok := sources[path]
			if !ok {
				return protocompile.SearchResult{}, os.ErrNotExist
			}
			return protocompile.SearchResult{Source: strings.NewReader(src)}, nil
		}),
	}
	files, err := c.Compile(t.Context(), roots...)
	require.NoError(t, err)
	require.Len(t, files, len(roots))
	return files
}
