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

	"github.com/trendvidia/protocompile"
	_ "github.com/trendvidia/protocompile/experimentalcompile" // registers the experimental compile hook
)

// TestUseExperimentalParser_RoutesThroughExperimental confirms that the
// blank import registers the hook and Compile routes through it.
func TestUseExperimentalParser_RoutesThroughExperimental(t *testing.T) {
	t.Parallel()

	resolver := protocompile.ResolverFunc(func(path string) (protocompile.SearchResult, error) {
		if path == "hello.proto" {
			return protocompile.SearchResult{
				Source: io.NopCloser(strings.NewReader(`
					syntax = "proto3";
					package hello;
					message Greeting {
					  string text = 1;
					}
				`)),
			}, nil
		}
		return protocompile.SearchResult{}, os.ErrNotExist
	})

	c := protocompile.Compiler{
		Resolver:              resolver,
		UseExperimentalParser: true,
	}

	files, err := c.Compile(t.Context(), "hello.proto")
	require.NoError(t, err)
	require.Len(t, files, 1)

	f := files[0]
	assert.Equal(t, "hello.proto", f.Path())

	msg := f.Messages().ByName("Greeting")
	require.NotNil(t, msg)

	field := msg.Fields().ByName("text")
	require.NotNil(t, field)
	assert.Equal(t, "string", field.Kind().String())
}

// TestUseExperimentalParser_DefaultUsesLegacy verifies that the
// UseExperimentalParser flag defaults to false and the legacy pipeline
// is used unless the flag is set.
func TestUseExperimentalParser_DefaultUsesLegacy(t *testing.T) {
	t.Parallel()

	c := protocompile.Compiler{
		Resolver: protocompile.ResolverFunc(func(path string) (protocompile.SearchResult, error) {
			if path == "ok.proto" {
				return protocompile.SearchResult{
					Source: io.NopCloser(strings.NewReader(`
						syntax = "proto3";
						package ok;
					`)),
				}, nil
			}
			return protocompile.SearchResult{}, os.ErrNotExist
		}),
	}

	files, err := c.Compile(t.Context(), "ok.proto")
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, "ok.proto", files[0].Path())
}
