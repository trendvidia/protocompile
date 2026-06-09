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

// Package experimentalcompile registers the experimental Compile
// implementation with the legacy [protocompile.Compiler] so that
// [protocompile.Compiler.UseExperimentalParser] becomes a working
// flag instead of returning an error.
//
// Usage is normally:
//
//	import _ "github.com/trendvidia/protocompile/experimentalcompile"
//
//	c := protocompile.Compiler{
//	    Resolver:              protocompile.WithStandardImports(&protocompile.SourceResolver{...}),
//	    UseExperimentalParser: true,
//	}
//	files, err := c.Compile(ctx, "foo.proto")
//
// The package's [Compile] function is also exported so callers that
// prefer an explicit entry point can use it without setting the flag.
//
// This indirection exists to break an import cycle: the experimental
// pipeline transitively depends on
// `github.com/trendvidia/protocompile/wellknownimports`, which depends
// on the legacy `protocompile` package, so the legacy package cannot
// itself import the experimental subpackages.
package experimentalcompile

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"

	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protocompile"
	"github.com/trendvidia/protocompile/experimental/fdp"
	"github.com/trendvidia/protocompile/experimental/incremental"
	"github.com/trendvidia/protocompile/experimental/incremental/queries"
	"github.com/trendvidia/protocompile/experimental/ir"
	"github.com/trendvidia/protocompile/experimental/report"
	"github.com/trendvidia/protocompile/experimental/source"
	"github.com/trendvidia/protocompile/linker"
)

func init() {
	protocompile.RegisterExperimentalCompile(Compile)
}

// Compile runs the experimental pipeline against the given files and
// returns the same [linker.Files] shape that the legacy compiler
// returns. The compiler's Resolver is adapted to a source.Opener for
// the experimental pipeline.
//
// This function is the registered implementation behind
// [protocompile.Compiler.UseExperimentalParser]. It can also be called
// directly when the explicit entry point is preferred.
func Compile(ctx context.Context, c *protocompile.Compiler, files []string) (linker.Files, error) {
	opener := experimentalOpener(c.Resolver)
	executor := incremental.New()
	session := &ir.Session{}

	qs := make([]incremental.Query[*ir.File], len(files))
	for i, f := range files {
		qs[i] = queries.IR{
			Opener:  opener,
			Session: session,
			Path:    f,
		}
	}

	results, rpt, err := incremental.Run(ctx, executor, qs...)
	if err != nil {
		return nil, err
	}

	for _, diag := range rpt.Diagnostics {
		if diag.Level() == report.Error || diag.Level() == report.ICE {
			return nil, fmt.Errorf("%v", diag)
		}
	}

	out := make(linker.Files, len(results))
	for i, r := range results {
		if r.Fatal != nil {
			return nil, fmt.Errorf("compilation failed for %s: %w", files[i], r.Fatal)
		}
		lf, err := irFileToLinkerFile(r.Value)
		if err != nil {
			return nil, fmt.Errorf("convert %s to linker.File: %w", files[i], err)
		}
		out[i] = lf
	}
	return out, nil
}

// irFileToLinkerFile converts an *ir.File into a linker.File by
// generating its FileDescriptorProto, building a protoreflect
// descriptor, and wrapping it via linker.NewFileRecursive.
func irFileToLinkerFile(file *ir.File) (linker.File, error) {
	if file == nil {
		return nil, errors.New("nil ir.File")
	}

	registry, topProto, err := buildExperimentalRegistry(file)
	if err != nil {
		return nil, err
	}

	// Round-trip the top-level FDP through the dynamicpb resolver so
	// extensions defined in this file (or its transitive imports)
	// surface as typed fields rather than unknown wire bytes.
	rawBytes, err := fdp.DescriptorProtoBytes(file)
	if err != nil {
		return nil, err
	}
	final := topProto
	if final == nil {
		final = new(descriptorpb.FileDescriptorProto)
	}
	resolver := dynamicpb.NewTypes(registry)
	typed := new(descriptorpb.FileDescriptorProto)
	if err := (protoUnmarshal{resolver: resolver}).do(rawBytes, typed); err == nil {
		final = typed
	}

	fd, err := protodesc.NewFile(final, chainResolver{registry, protoregistry.GlobalFiles})
	if err != nil {
		return nil, err
	}
	return linker.NewFileRecursive(fd)
}

// buildExperimentalRegistry collects the file and its transitive
// imports into a *protoregistry.Files. Returns the registry and the
// top-level file's freshly generated FileDescriptorProto.
func buildExperimentalRegistry(top *ir.File) (*protoregistry.Files, *descriptorpb.FileDescriptorProto, error) {
	files := new(protoregistry.Files)

	var topProto *descriptorpb.FileDescriptorProto
	registered := make(map[string]bool)
	var register func(irFile *ir.File, isTop bool) error
	register = func(irFile *ir.File, isTop bool) error {
		path := irFile.Path()
		if registered[path] {
			return nil
		}
		registered[path] = true

		// Transitive imports first; failures are tolerated.
		imports := irFile.Imports()
		for i := range imports.Len() {
			_ = register(imports.At(i).File, false)
		}

		// Skip files already in the global registry (WKTs).
		if _, err := protoregistry.GlobalFiles.FindFileByPath(path); err == nil {
			return nil
		}

		dep, err := fdp.DescriptorProto(irFile)
		if err != nil || dep == nil {
			return err
		}
		if isTop {
			topProto = dep
		}

		fd, err := protodesc.NewFile(dep, chainResolver{files, protoregistry.GlobalFiles})
		if err != nil {
			return err
		}
		return files.RegisterFile(fd)
	}

	if err := register(top, true); err != nil {
		return nil, nil, err
	}
	return files, topProto, nil
}

// experimentalOpener adapts a protocompile.Resolver to a
// source.Opener. WKTs are served from source.WKTs(); other paths flow
// through the supplied resolver. Result types other than Source are
// surfaced as not-found for now.
func experimentalOpener(resolver protocompile.Resolver) source.Opener {
	wkts := source.WKTs()
	if resolver == nil {
		return wkts
	}
	return &source.Openers{wkts, &resolverOpener{resolver: resolver}}
}

type resolverOpener struct {
	resolver protocompile.Resolver
}

func (r *resolverOpener) Open(path string) (*source.File, error) {
	result, err := r.resolver.FindFileByPath(path)
	if err != nil {
		return nil, err
	}
	if result.Source != nil {
		data, err := io.ReadAll(result.Source)
		if err != nil {
			return nil, err
		}
		return source.NewFile(path, string(data)), nil
	}
	return nil, fs.ErrNotExist
}

// chainResolver tries each Resolver in order. Lets protodesc.NewFile
// find a dependency in the per-compile registry first and fall back
// to the global registry for WKTs.
type chainResolver struct {
	primary, secondary protodesc.Resolver
}

func (c chainResolver) FindFileByPath(path string) (protoreflect.FileDescriptor, error) {
	if fd, err := c.primary.FindFileByPath(path); err == nil {
		return fd, nil
	}
	return c.secondary.FindFileByPath(path)
}

func (c chainResolver) FindDescriptorByName(name protoreflect.FullName) (protoreflect.Descriptor, error) {
	if d, err := c.primary.FindDescriptorByName(name); err == nil {
		return d, nil
	}
	return c.secondary.FindDescriptorByName(name)
}

// protoUnmarshal wraps proto.UnmarshalOptions with a custom resolver
// so we can keep the proto import out of this file's main flow and
// localise the dependency at one call site.
type protoUnmarshal struct {
	resolver protoregistry.ExtensionTypeResolver
}

func (p protoUnmarshal) do(data []byte, m protoreflect.ProtoMessage) error {
	return protoUnmarshalImpl(p.resolver, data, m)
}
