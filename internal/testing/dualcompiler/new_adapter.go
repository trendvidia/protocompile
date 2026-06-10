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
	"context"
	"fmt"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protocompile/experimental/fdp"
	"github.com/trendvidia/protocompile/experimental/incremental"
	"github.com/trendvidia/protocompile/experimental/incremental/queries"
	"github.com/trendvidia/protocompile/experimental/ir"
	"github.com/trendvidia/protocompile/experimental/report"
	"github.com/trendvidia/protocompile/experimental/source"
)

// newCompilerAdapter wraps the experimental incremental compiler.
type newCompilerAdapter struct {
	executor              *incremental.Executor
	opener                source.Opener
	session               *ir.Session
	includeSourceCodeInfo bool
}

// NewNewCompiler creates a new CompilerInterface wrapping the experimental compiler.
// Use WithResolver option to specify a custom resolver.
// The resolver will be converted to an Opener and combined with WKTs.
func NewNewCompiler(opts ...CompilerOption) CompilerInterface {
	config := &compilerConfig{}
	for _, opt := range opts {
		opt(config)
	}

	// Create an opener that consults the user's resolver first, then
	// falls back to the baked-in WKTs. This matches the production
	// experimentalcompile.experimentalOpener ordering and lets fixtures
	// vendor partial overrides of well-known types (e.g. the HACK fields
	// in `internal/testdata/options/google/protobuf/descriptor.proto`).
	var opener source.Opener
	if config.resolver != nil {
		resolverOpener := ResolverToOpener(config.resolver)
		wkts := source.WKTs()
		opener = &source.Openers{resolverOpener, wkts}
	} else {
		opener = source.WKTs()
	}

	var includeSourceCodeInfo bool
	if config.sourceInfoMode != 0 {
		includeSourceCodeInfo = true
	}

	return &newCompilerAdapter{
		executor:              incremental.New(),
		opener:                opener,
		session:               &ir.Session{},
		includeSourceCodeInfo: includeSourceCodeInfo,
	}
}

// Name implements CompilerInterface.
func (a *newCompilerAdapter) Name() string {
	return "new_compiler"
}

// Compile implements CompilerInterface.
func (a *newCompilerAdapter) Compile(ctx context.Context, files ...string) (CompilationResult, error) {
	// Create IR queries for each file
	qs := make([]incremental.Query[*ir.File], len(files))
	for i, file := range files {
		qs[i] = queries.IR{
			Opener:  a.opener,
			Session: a.session,
			Path:    file,
		}
	}

	// Run the queries
	results, rpt, err := incremental.Run(ctx, a.executor, qs...)
	if err != nil {
		return nil, err
	}

	// Check for fatal errors in individual results
	irFiles := make([]*ir.File, 0, len(results))
	for i, result := range results {
		if result.Fatal != nil {
			return nil, fmt.Errorf("compilation failed for %s: %w", files[i], result.Fatal)
		}
		irFiles = append(irFiles, result.Value)
	}

	// Check for errors in the report
	for _, diag := range rpt.Diagnostics {
		if diag.Level() == report.Error || diag.Level() == report.ICE {
			return nil, fmt.Errorf("%v", diag)
		}
	}

	return &newCompilationResult{
		files:                 irFiles,
		includeSourceCodeInfo: a.includeSourceCodeInfo,
	}, nil
}

// newCompilationResult wraps IR files.
type newCompilationResult struct {
	files                 []*ir.File
	includeSourceCodeInfo bool
}

// Files implements CompilationResult.
func (r *newCompilationResult) Files() []CompiledFile {
	result := make([]CompiledFile, len(r.files))
	for i, file := range r.files {
		result[i] = &newCompiledFile{
			file:                  file,
			includeSourceCodeInfo: r.includeSourceCodeInfo,
		}
	}
	return result
}

// newCompiledFile wraps an ir.File.
type newCompiledFile struct {
	file                  *ir.File
	includeSourceCodeInfo bool
}

// Path implements CompiledFile.
func (f *newCompiledFile) Path() string {
	return f.file.Path()
}

// FileDescriptor implements CompiledFile.
// Converts the FileDescriptorProto to a FileDescriptor using protodesc.
// Dependencies are resolved using the global registry (includes WKTs and other registered files).
func (f *newCompiledFile) FileDescriptor() (protoreflect.FileDescriptor, error) {
	fdp, err := f.FileDescriptorProto()
	if err != nil {
		return nil, err
	}

	return protodesc.NewFile(fdp, protoregistry.GlobalFiles)
}

// FileDescriptorProto implements CompiledFile.
//
// Two-pass unmarshal: the first pass uses the global resolver to give
// us the file's structure (messages, enums, fields, options
// containing only standard fields and globally-registered extensions).
// We then build a per-file extension resolver from the descriptor's
// own definitions and re-unmarshal so that options like
// `[testprotos.flfubar]` come back as typed extension fields instead
// of raw unknown bytes. Without this step, proto.Equal and protocmp
// comparisons against the legacy compiler's output spuriously diverge
// because the legacy adapter returns a proto whose extensions are
// already typed (it pulls the proto from a linker.File that carries
// its own extension types).
func (f *newCompiledFile) FileDescriptorProto() (*descriptorpb.FileDescriptorProto, error) {
	data, err := fdp.DescriptorProtoBytes(
		f.file,
		fdp.IncludeSourceCodeInfo(f.includeSourceCodeInfo),
	)
	if err != nil {
		return nil, err
	}

	raw := &descriptorpb.FileDescriptorProto{}
	if err := proto.Unmarshal(data, raw); err != nil {
		return nil, err
	}

	// Build a Files registry containing this file and its transitive
	// imports. If anything fails, fall back to the raw FDP — we'd
	// rather return a result with untyped extensions than fail the
	// compile.
	files, err := buildFileRegistry(f.file, raw)
	if err != nil {
		return raw, nil
	}

	resolver := dynamicpb.NewTypes(files)
	final := &descriptorpb.FileDescriptorProto{}
	if err := (proto.UnmarshalOptions{Resolver: resolver}).Unmarshal(data, final); err != nil {
		return raw, nil
	}
	return final, nil
}

// buildFileRegistry collects the file and all of its transitive
// imports into a protoregistry.Files, generating each dependency's
// FileDescriptorProto on the fly via the experimental fdp generator.
// The registry serves the dynamicpb extension-type resolver so that
// extensions defined anywhere in the dependency tree resolve to typed
// fields when the top-level descriptor is unmarshaled.
//
// Registration is best-effort: a failure on one dependency does not
// abort the others, since the resolver only needs to know about the
// extensions actually referenced from the top-level file. The top-
// level file's own registration error is propagated, because without
// it the resolver provides no value over the global registry.
func buildFileRegistry(file *ir.File, raw *descriptorpb.FileDescriptorProto) (*protoregistry.Files, error) {
	files := new(protoregistry.Files)

	registered := make(map[string]bool)
	var register func(irFile *ir.File, top bool) error
	register = func(irFile *ir.File, top bool) error {
		path := irFile.Path()
		if registered[path] {
			return nil
		}
		registered[path] = true

		// Register dependencies first so RegisterFile can find them.
		// Failures on transitive imports are tolerated.
		imports := irFile.Imports()
		for i := range imports.Len() {
			_ = register(imports.At(i).File, false)
		}

		// Skip files already in the global registry (WKTs like
		// descriptor.proto) so RegisterFile does not error on
		// duplicates.
		if _, err := protoregistry.GlobalFiles.FindFileByPath(path); err == nil {
			return nil
		}

		// Reuse the already-built raw FDP for the top-level file; ask
		// the fdp generator for a fresh one on each dependency.
		var dep *descriptorpb.FileDescriptorProto
		if top {
			dep = raw
		} else {
			d, err := fdp.DescriptorProto(irFile)
			if err != nil || d == nil {
				return err
			}
			dep = d
		}

		// The fdp generator parks every options message under
		// `descriptorpb.X.SetUnknown(...)`, so typed fields like
		// `MessageOptions.map_entry` stay nil even though the wire
		// bytes carry the value. That nil propagates through
		// protodesc.NewFile into the registered FileDescriptor, and
		// downstream dynamicpb users no longer recognise map-entry
		// types as maps — surfacing later as e.g. `map[uint64]uint64`
		// (legacy) versus `[]protocmp.Message` (experimental) in
		// protocmp diffs. Re-serialise and unmarshal via the
		// in-progress registry so the typed fields surface before
		// protodesc sees them.
		depBytes, err := fdp.DescriptorProtoBytes(irFile)
		if err == nil {
			typedDep := &descriptorpb.FileDescriptorProto{}
			depResolver := dynamicpb.NewTypes(files)
			if err := (proto.UnmarshalOptions{Resolver: depResolver}).Unmarshal(depBytes, typedDep); err == nil {
				dep = typedDep
			}
		}

		// Resolve dependencies against the registry being built (for
		// files we've registered above) chained with the global
		// registry (for WKTs like descriptor.proto).
		fd, err := protodesc.NewFile(dep, chainedFiles{files, protoregistry.GlobalFiles})
		if err != nil {
			return err
		}
		return files.RegisterFile(fd)
	}

	if err := register(file, true); err != nil {
		return nil, err
	}
	return files, nil
}

// chainedFiles tries each Files in order when resolving a file by path
// or descriptor by name. Used so protodesc.NewFile can resolve a
// dependency against the per-compile registry first and fall back to
// the global registry for WKTs.
type chainedFiles struct {
	primary, secondary protodesc.Resolver
}

func (c chainedFiles) FindFileByPath(path string) (protoreflect.FileDescriptor, error) {
	if fd, err := c.primary.FindFileByPath(path); err == nil {
		return fd, nil
	}
	return c.secondary.FindFileByPath(path)
}

func (c chainedFiles) FindDescriptorByName(name protoreflect.FullName) (protoreflect.Descriptor, error) {
	if d, err := c.primary.FindDescriptorByName(name); err == nil {
		return d, nil
	}
	return c.secondary.FindDescriptorByName(name)
}
