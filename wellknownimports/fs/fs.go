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

// Package fs hosts the embedded source code for the well-known import
// files (`google/protobuf/*.proto`) and the matching pre-built
// FileDescriptorSet, packaged so consumers can pick them up *without*
// pulling in the `protocompile` root package.
//
// The parent `wellknownimports` package still re-exports [FS] and
// [Files] for backwards compatibility, and additionally provides
// `WithStandardImports` (which wraps [github.com/trendvidia/protocompile.Resolver]
// and therefore depends on the root package). Keeping this sub-package
// dependency-free is what lets the experimental compiler (which
// transitively imports the embedded files) be imported by the root
// `protocompile` package without an import cycle.
package fs

import (
	"embed"
	"io/fs"
	"sync"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

//go:embed google/protobuf/*.proto google/protobuf/*/*.proto
var files embed.FS

// FS returns a filesystem over the built-in well-known imports.
func FS() fs.FS {
	return files
}

var (
	//go:embed wkt.pb
	encoded  []byte
	registry = sync.OnceValue(func() *protoregistry.Files {
		fds := new(descriptorpb.FileDescriptorSet)
		if err := proto.Unmarshal(encoded, fds); err != nil {
			panic(err)
		}

		reg, err := protodesc.NewFiles(fds)
		if err != nil {
			panic(err)
		}

		return reg
	})
)

// Files returns reflection information for the WKTs included with
// protocompile, which are not the ones bundled with protoreflect.
func Files() *protoregistry.Files {
	return registry()
}
