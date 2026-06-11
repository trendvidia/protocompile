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

package dualcompiler

import (
	"io"
	"io/fs"

	"github.com/trendvidia/protocompile"
	"github.com/trendvidia/protocompile/source"
)

// resolverOpener adapts a protocompile.Resolver to the source.Opener interface
// used by the experimental compiler.
type resolverOpener struct {
	resolver protocompile.Resolver
}

// ResolverToOpener converts a Resolver to an Opener.
// Note: This adapter only supports SearchResult.Source. Other result types
// (AST, Proto, ParseResult, Desc) will return an error.
func ResolverToOpener(resolver protocompile.Resolver) source.Opener {
	return &resolverOpener{resolver: resolver}
}

// Open implements source.Opener.
func (r *resolverOpener) Open(path string) (*source.File, error) {
	result, err := r.resolver.FindFileByPath(path)
	if err != nil {
		return nil, err
	}

	// Handle the Source result type (most common in tests)
	if result.Source != nil {
		data, err := io.ReadAll(result.Source)
		if err != nil {
			return nil, err
		}
		return source.NewFile(path, string(data)), nil
	}

	// For Proto/Desc, return ErrNotExist so the Openers chain can
	// fall back to the next opener (WKTs source files). The
	// experimental pipeline needs source bytes, not pre-built
	// descriptors.
	if result.Proto != nil {
		return nil, fs.ErrNotExist
	}
	if result.Desc != nil {
		return nil, fs.ErrNotExist
	}

	// No result found
	return nil, fs.ErrNotExist
}
