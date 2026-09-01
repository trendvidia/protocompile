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

// Package wellknownimports provides source code for the well-known import
// files for use with a protocompile.Compiler.
package wellknownimports

import (
	"io"
	wktfs "io/fs"

	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/trendvidia/protocompile"
	imports "github.com/trendvidia/protocompile/wellknownimports/fs"
)

// FS returns a filesystem over the built-in well-known imports.
//
// The actual embed lives in [github.com/trendvidia/protocompile/wellknownimports/fs];
// the parent package re-exports it for backwards compatibility. Code
// that does not need `WithStandardImports` (which is the only part of
// this package that pulls in `protocompile.Resolver` and friends)
// should import the sub-package directly so the dependency on the
// root `protocompile` package can be avoided.
func FS() wktfs.FS {
	return imports.FS()
}

// Files returns reflection information for the WKTs included with protocompile,
// which are not the ones bundled with protoreflect.
//
// Like [FS], this re-exports the implementation in
// [github.com/trendvidia/protocompile/wellknownimports/fs].
func Files() *protoregistry.Files {
	return imports.Files()
}

// WithStandardImports returns a new resolver that can provide the source code for the
// standard imports that are included with protoc, from the copies embedded in this
// module.
//
// [github.com/trendvidia/protocompile.WithStandardImports] is now equivalent: it
// serves the same embedded source for the same set of paths, and like this one it
// substitutes only where the wrapped resolver fails. It used to answer with runtime
// descriptors from generated code instead, which is where the differences this
// comment previously described came from — a possible version skew against the
// Protobuf Go module, and the absence of the extension declarations that keep a
// source file from re-defining the C++/Java/Go feature extensions. Neither applies
// now; see #155.
//
// Note also that the compiler falls back to these same embedded files for an import
// no resolver answers, so wrapping is only needed when a resolver reports an error
// for a standard path rather than a miss.
func WithStandardImports(resolver protocompile.Resolver) protocompile.Resolver {
	return protocompile.CompositeResolver{
		resolver,
		&protocompile.SourceResolver{
			Accessor: func(path string) (io.ReadCloser, error) {
				return imports.FS().Open(path)
			},
		},
	}
}
