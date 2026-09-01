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
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/trendvidia/protocompile/internal/descsrc"
	"github.com/trendvidia/protocompile/internal/exphook"
	"github.com/trendvidia/protocompile/linker"
	"github.com/trendvidia/protocompile/reporter"
)

// Compiler turns protobuf source files into fully linked descriptors via
// the experimental parser/ir/fdp pipeline. (The legacy
// parser/linker/options/sourceinfo pipeline was deleted in Track C of
// the M1 migration.)
type Compiler struct {
	// Resolver locates source code for the files to be compiled and
	// their dependencies. The only field of [SearchResult] that the
	// experimental pipeline honours is `Source`.
	Resolver Resolver

	// MaxParallelism is reserved for forwards compatibility and is
	// currently ignored — the experimental pipeline manages its own
	// parallelism via the incremental query executor.
	MaxParallelism int

	// Reporter receives error and warning diagnostics, with source
	// positions attached. If nil, a default reporter is used that fails
	// compilation on the first error and ignores warnings.
	//
	// The legacy reporter contract holds: if the reporter's Error
	// method returns nil, compilation continues so that as many
	// diagnostics as possible are reported, and Compile returns the
	// files that did compile — with nil placeholders in the slots of
	// files that didn't — alongside [reporter.ErrInvalidSource]. If the
	// Error method returns a non-nil error, the batch aborts
	// immediately with that error.
	Reporter reporter.Reporter

	// SourceInfoMode controls whether and how source-code-info is
	// emitted in the resulting descriptors.
	SourceInfoMode SourceInfoMode

	// Symbols, when non-nil, accumulates every compiled file. A
	// duplicate-symbol collision across this and previous Compile
	// calls surfaces as an error.
	Symbols *linker.Symbols

	// RetainASTs causes the returned [linker.File] values to also
	// implement `experimentalcompile.IRHolder`, exposing the
	// experimental IR (and AST) via a type assertion. If false, ASTs
	// may be garbage-collected after Compile returns.
	RetainASTs bool
}

// RegisterExperimentalCompile is preserved as a compatibility shim for
// pre-Track-C callers that wired the experimental pipeline by hand.
// It now delegates to [exphook.Register]; new code should use
// [exphook.Register] (via a blank import of `experimentalcompile`).
//
// Deprecated: use a blank import of
// `github.com/trendvidia/protocompile/experimentalcompile`. This
// function will be removed in a future release.
func RegisterExperimentalCompile(fn func(ctx context.Context, c *Compiler, files []string) (linker.Files, error)) {
	if fn == nil {
		exphook.Register(nil)
		return
	}
	exphook.Register(func(ctx context.Context, args exphook.Args, files []string) (linker.Files, error) {
		return fn(ctx, &Compiler{
			Resolver:       legacyResolverShim{resolver: args.Resolver},
			Symbols:        args.Symbols,
			Reporter:       args.Reporter,
			SourceInfoMode: SourceInfoMode(args.SourceInfoMode),
			RetainASTs:     args.RetainASTs,
		}, files)
	})
}

// legacyResolverShim wraps an [exphook.Resolver] into a [Resolver] for
// backwards compatibility with the deprecated
// [RegisterExperimentalCompile] entry point.
type legacyResolverShim struct {
	resolver exphook.Resolver
}

func (l legacyResolverShim) FindFileByPath(path string) (SearchResult, error) {
	src, err := l.resolver.OpenSource(path)
	if err != nil {
		return SearchResult{}, err
	}
	if src == nil {
		return SearchResult{}, nil
	}
	rc, ok := src.(io.ReadCloser)
	if !ok {
		rc = io.NopCloser(src)
	}
	return SearchResult{Source: rc}, nil
}

// expArgs builds an [exphook.Args] from this Compiler.
func (c *Compiler) expArgs() exphook.Args {
	return exphook.Args{
		Resolver:       expResolverAdapter{resolver: c.Resolver},
		Symbols:        c.Symbols,
		Reporter:       c.Reporter,
		SourceInfoMode: int(c.SourceInfoMode),
		RetainASTs:     c.RetainASTs,
	}
}

// expResolverAdapter adapts a [Resolver] to the [exphook.Resolver]
// interface that the experimental pipeline consumes.
//
// The pipeline reads source bytes and nothing else. A resolver that
// answers with a descriptor instead — [SearchResult.Desc] or
// [SearchResult.Proto], the shape of the `protoregistry.GlobalFiles`
// pattern — is honoured by rendering that descriptor back to source.
type expResolverAdapter struct {
	resolver Resolver
}

func (a expResolverAdapter) OpenSource(path string) (io.Reader, error) {
	if a.resolver == nil {
		return nil, nil
	}
	res, err := a.resolver.FindFileByPath(path)
	if err != nil {
		return nil, err
	}
	if res.Source != nil {
		return res.Source, nil
	}

	fdp := searchResultDescriptor(res)
	if fdp == nil {
		// A wholly empty SearchResult is how a resolver says not-found.
		return nil, nil
	}

	src, err := descsrc.Render(fdp)
	if err != nil {
		// Never fall through to not-found here. The resolver did answer;
		// reporting "imported file does not exist" would blame the
		// importing file for a problem in this descriptor.
		return nil, fmt.Errorf("protocompile: resolver returned a descriptor for %q that cannot be rendered as source: %w", path, err)
	}
	return strings.NewReader(src), nil
}

// searchResultDescriptor extracts the FileDescriptorProto a SearchResult
// carries, or nil when it carries neither descriptor form.
func searchResultDescriptor(res SearchResult) *descriptorpb.FileDescriptorProto {
	if res.Proto != nil {
		return res.Proto
	}
	if res.Desc != nil {
		return protodesc.ToFileDescriptorProto(res.Desc)
	}
	return nil
}

// SourceInfoMode indicates how source code info is generated by a Compiler.
type SourceInfoMode int

const (
	// SourceInfoNone indicates that no source code info is generated.
	SourceInfoNone = SourceInfoMode(0)
	// SourceInfoStandard indicates that the standard source code info is
	// generated, which includes comments only for complete declarations.
	SourceInfoStandard = SourceInfoMode(1)
	// SourceInfoExtraComments is currently treated the same as
	// SourceInfoStandard on the experimental pipeline.
	SourceInfoExtraComments = SourceInfoMode(2)
	// SourceInfoExtraOptionLocations indicates that source code info is
	// generated with additional locations for elements inside of message
	// literals in option values. This can be combined with the above by
	// bitwise-OR'ing.
	SourceInfoExtraOptionLocations = SourceInfoMode(4)
)

// Compile compiles the given file names into fully-linked descriptors.
// The compiler's [Resolver] is used to locate source code; the
// experimental pipeline reads the resolver's `Source` field.
//
// Elements in the returned files implement
// `experimentalcompile.IRHolder` when [Compiler.RetainASTs] is true.
func (c *Compiler) Compile(ctx context.Context, files ...string) (linker.Files, error) {
	if len(files) == 0 {
		return nil, nil
	}
	fn := exphook.Get()
	if fn == nil {
		return nil, errors.New(
			"protocompile: experimental compile hook is not registered; " +
				`blank-import "github.com/trendvidia/protocompile/experimentalcompile" ` +
				"to wire it up",
		)
	}
	return fn(ctx, c.expArgs(), files)
}

// PanicError is preserved for backwards compatibility with callers
// that referenced it. The experimental pipeline reports panics as
// internal-compiler-error diagnostics through the [Reporter] instead
// of returning a PanicError, so this type is no longer produced by
// [Compile].
//
// Deprecated: PanicError will be removed in a future release.
type PanicError struct {
	File  string
	Value any
	Stack string
}

// Error implements the error interface.
func (e PanicError) Error() string {
	return "panic in " + e.File
}
