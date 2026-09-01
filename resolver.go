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

package protocompile

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	imports "github.com/trendvidia/protocompile/wellknownimports/fs"
)

// Resolver is used by the compiler to resolve a proto source file name
// into some unit that is usable by the compiler. The result could be source
// for a proto file or it could be an already-parsed AST or descriptor.
//
// Resolver implementations must be thread-safe as a single compilation
// operation could invoke FindFileByPath from multiple goroutines.
type Resolver interface {
	// FindFileByPath searches for information for the given file path. If no
	// result is available, it should return a non-nil error, such as
	// protoregistry.NotFound.
	FindFileByPath(path string) (SearchResult, error)
}

// SearchResult represents information about a proto source file.
//
// A resolver answers with exactly one of the fields below; a wholly zero
// SearchResult (with no error) means not-found.
//
// After Track C of the M1 migration the pipeline itself reads source bytes
// and nothing else. `Proto` and `Desc` remain supported: a descriptor
// supplied through either is rendered back to source and compiled. That
// rendering is all-or-nothing — a descriptor carrying something that cannot
// be expressed in source produces an error naming the file and the reason,
// never a silent not-found.
//
// The pre-Track-C `AST` and `ParseResult` fields, which carried the
// legacy AST and parser-result types, were removed along with the
// legacy `ast/` and `parser/` packages.
type SearchResult struct {
	// Source carries the file's source bytes. The pipeline reads from this
	// and treats a nil reader (with no error) as not-found.
	//
	// When set, Source wins: Proto and Desc are not consulted.
	Source io.Reader
	// Proto carries the file as a descriptor, which is rendered back to
	// source and compiled. Used only when Source is nil.
	Proto *descriptorpb.FileDescriptorProto
	// Desc carries the file as a linked descriptor — the shape of the
	// `protoregistry.GlobalFiles` pattern — which is rendered back to
	// source and compiled. Used only when Source and Proto are nil.
	Desc protoreflect.FileDescriptor
}

// ResolverFunc is a simple function type that implements Resolver.
type ResolverFunc func(string) (SearchResult, error)

var _ Resolver = ResolverFunc(nil)

func (f ResolverFunc) FindFileByPath(path string) (SearchResult, error) {
	return f(path)
}

// CompositeResolver is a slice of resolvers, which are consulted in order
// until one can supply a result. If none of the constituent resolvers can
// supply a result, the error returned by the first resolver is returned. If
// the slice of resolvers is empty, all operations return
// protoregistry.NotFound.
type CompositeResolver []Resolver

var _ Resolver = CompositeResolver(nil)

func (f CompositeResolver) FindFileByPath(path string) (SearchResult, error) {
	if len(f) == 0 {
		return SearchResult{}, protoregistry.NotFound
	}
	var firstErr error
	for _, res := range f {
		r, err := res.FindFileByPath(path)
		if err == nil {
			return r, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return SearchResult{}, firstErr
}

// SourceResolver can resolve file names by returning source code. It uses
// an optional list of import paths to search. By default, it searches the
// file system.
type SourceResolver struct {
	// Optional list of import paths. If present and not empty, then all
	// file paths to find are assumed to be relative to one of these paths.
	// If nil or empty, all file paths to find are assumed to be relative to
	// the current working directory.
	ImportPaths []string
	// Optional function for returning a file's contents. If nil, then
	// os.Open is used to open files on the file system.
	//
	// This function must be thread-safe as a single compilation operation
	// could result in concurrent invocations of this function from
	// multiple goroutines.
	Accessor func(path string) (io.ReadCloser, error)
}

var _ Resolver = (*SourceResolver)(nil)

func (r *SourceResolver) FindFileByPath(path string) (SearchResult, error) {
	if len(r.ImportPaths) == 0 {
		reader, err := r.accessFile(path)
		if err != nil {
			return SearchResult{}, err
		}
		return SearchResult{Source: reader}, nil
	}

	var e error
	for _, importPath := range r.ImportPaths {
		reader, err := r.accessFile(filepath.Join(importPath, path))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				e = err
				continue
			}
			return SearchResult{}, err
		}
		return SearchResult{Source: reader}, nil
	}
	return SearchResult{}, e
}

func (r *SourceResolver) accessFile(path string) (io.ReadCloser, error) {
	if r.Accessor != nil {
		return r.Accessor(path)
	}
	return os.Open(path)
}

// SourceAccessorFromMap returns a function that can be used as the Accessor
// field of a SourceResolver that uses the given map to load source. The map
// keys are file names and the values are the corresponding file contents.
//
// The given map is used directly and not copied. Since accessor functions
// must be thread-safe, this means that the provided map must not be mutated
// once this accessor is provided to a compile operation.
func SourceAccessorFromMap(srcs map[string]string) func(string) (io.ReadCloser, error) {
	return func(path string) (io.ReadCloser, error) {
		src, ok := srcs[path]
		if !ok {
			return nil, os.ErrNotExist
		}
		return io.NopCloser(strings.NewReader(src)), nil
	}
}

// WithStandardImports returns a new resolver that knows about the same standard
// imports that are included with protoc.
//
// The standard files are served as **source**, from the copies embedded in
// this module, so they carry everything source carries — including the
// extension declarations on "google/protobuf/descriptor.proto" that prevent
// a source file from illegally re-defining the custom features for C++,
// Java, and Go.
//
// It previously answered with runtime descriptors from generated code, which
// do not retain extension declarations — those exist only in source. While
// [SearchResult.Desc] was inert that made no difference, because the
// compiler's own built-in copies served these paths instead. Once Desc was
// honoured the declaration-less descriptors would have won and silently
// dropped that guard, so the source form is served directly (see #155).
//
// The set of paths served is unchanged. This now behaves equivalently to
// [github.com/trendvidia/protocompile/wellknownimports.WithStandardImports],
// which composes a source resolver over the same embedded files; prefer
// either, and note that the compiler already falls back to these files for
// an import no resolver answers.
func WithStandardImports(r Resolver) Resolver {
	return ResolverFunc(func(name string) (SearchResult, error) {
		res, err := r.FindFileByPath(name)
		if err == nil {
			return res, nil
		}
		// Error from the given resolver? See if it is a known standard file.
		// standardImports still decides membership, so the set of paths this
		// answers for is exactly what it was.
		if _, ok := standardImports[name]; !ok {
			return res, err
		}
		src, srcErr := standardImportSource(name)
		if srcErr != nil {
			// Surface the caller's own error rather than masking it with one
			// about our embedded copy.
			return res, err
		}
		return SearchResult{Source: src}, nil
	})
}

// standardImportSource reads one embedded standard import into memory. The
// bytes are read eagerly so the returned reader outlives the file handle.
func standardImportSource(name string) (io.Reader, error) {
	f, err := imports.FS().Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(b), nil
}
