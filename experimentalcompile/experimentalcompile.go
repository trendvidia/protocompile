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

	"github.com/trendvidia/protocompile/fdp"
	"github.com/trendvidia/protocompile/incremental"
	"github.com/trendvidia/protocompile/incremental/queries"
	"github.com/trendvidia/protocompile/internal/exphook"
	"github.com/trendvidia/protocompile/ir"
	"github.com/trendvidia/protocompile/linker"
	"github.com/trendvidia/protocompile/report"
	"github.com/trendvidia/protocompile/reporter"
	"github.com/trendvidia/protocompile/source"
)

func init() {
	exphook.Register(Compile)
}

// Compile runs the experimental pipeline against the given files and
// returns the same [linker.Files] shape that the legacy compiler
// returns. The supplied [exphook.Args] is filled by the root
// `protocompile` package from a `*protocompile.Compiler` before
// dispatch; callers that prefer the explicit entry point can construct
// Args themselves.
//
// Diagnostics produced by the pipeline are routed through
// `args.Reporter` as positioned [reporter.ErrorWithPos] values,
// preserving the legacy reporter contract: an error reporter that
// returns nil lets the batch continue (so every diagnostic gets
// reported) and Compile returns the files that did compile — with nil
// placeholders in the slots of files that didn't — alongside
// [reporter.ErrInvalidSource]; an error reporter that returns non-nil
// aborts immediately with that error.
func Compile(ctx context.Context, args exphook.Args, files []string) (linker.Files, error) {
	opener := experimentalOpener(args.Resolver)
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

	// Route every diagnostic through the caller's reporter. The handler
	// implements the legacy contract on top of it: an Error callback
	// returning non-nil aborts the batch with that error, returning nil
	// keeps reporting, and the batch then fails with ErrInvalidSource
	// once all diagnostics are delivered. The same handler also serves
	// the Symbols.Import collision path below so a single Compile
	// reports through one consistent stream.
	handler := reporter.NewHandler(args.Reporter)
	for i := range rpt.Diagnostics {
		diag := &rpt.Diagnostics[i]
		switch diag.Level() {
		case report.Error, report.ICE:
			if err := handler.HandleError(diagErrorWithPos(diag)); err != nil {
				return nil, err
			}
		case report.Warning:
			handler.HandleWarning(diagErrorWithPos(diag))
		case report.Remark:
			// Remarks have no legacy reporter equivalent.
		}
	}
	// True when error diagnostics were reported but the reporter
	// swallowed all of them; per-file failures below then degrade to
	// nil placeholders instead of failing the whole batch.
	errsReported := handler.Error() != nil

	fdpOpts := fdpOptionsFor(args.SourceInfoMode)

	out := make(linker.Files, len(results))
	for i, r := range results {
		if r.Fatal != nil {
			if errsReported {
				// Already reported as a diagnostic above; keep the rest
				// of the batch alive.
				continue
			}
			return nil, fmt.Errorf("compilation failed for %s: %w", files[i], r.Fatal)
		}
		lf, err := irFileToLinkerFile(r.Value, fdpOpts)
		if err != nil {
			if errsReported {
				// This file (or one of its imports) had reported errors,
				// so its partial IR need not convert cleanly.
				continue
			}
			return nil, fmt.Errorf("convert %s to linker.File: %w", files[i], err)
		}
		// Honor `Symbols`: when non-nil, each compiled file is imported
		// into the shared symbol table so collisions across this Compile
		// call and previous ones are reported.
		if args.Symbols != nil {
			if err := args.Symbols.Import(lf, handler); err != nil {
				return nil, fmt.Errorf("symbol collision in %s: %w", files[i], err)
			}
		}
		// Honor `RetainASTs`: when set, the returned linker.File also
		// implements [IRHolder] so callers can recover the experimental
		// [*ir.File] (and through it the AST). The flag matches the
		// legacy compiler's semantics — the AST is retained for further
		// processing only when the caller explicitly opts in.
		if args.RetainASTs {
			lf = &irHoldingFile{File: lf, ir: r.Value}
		}
		out[i] = lf
	}

	// Legacy partial-result semantics: when errors were reported but
	// the reporter chose to continue, hand back what did compile along
	// with the handler's terminal error (ErrInvalidSource, or whatever
	// a collision import reported).
	if err := handler.Error(); err != nil {
		return out, err
	}
	return out, nil
}

// diagErrorWithPos converts an experimental [report.Diagnostic] into
// the legacy [reporter.ErrorWithPos] shape. Positions come from the
// diagnostic's primary span when it has one; diagnostics without a
// span (e.g. "file not found") carry only the file name.
func diagErrorWithPos(diag *report.Diagnostic) reporter.ErrorWithPos {
	err := errors.New(diag.Message())
	span := diag.Primary()
	if span.IsZero() {
		return reporter.Error(reporter.UnknownSpan(diag.File()), err)
	}
	start, end := span.StartLoc(), span.EndLoc()
	path := span.File.Path()
	return reporter.Error(reporter.NewSourceSpan(
		reporter.SourcePos{Filename: path, Line: start.Line, Col: start.Column, Offset: start.Offset},
		reporter.SourcePos{Filename: path, Line: end.Line, Col: end.Column, Offset: end.Offset},
	), err)
}

// IRHolder is the interface a [linker.File] returned by [Compile]
// satisfies when [protocompile.Compiler.RetainASTs] is true. Callers
// who want to introspect the experimental IR (and through it the
// experimental AST) recover it via a type assertion:
//
//	for _, f := range files {
//	    if h, ok := f.(experimentalcompile.IRHolder); ok {
//	        irFile := h.IR()
//	        // irFile.AST() returns the experimental ast.File.
//	    }
//	}
//
// When RetainASTs is false, the returned linker.File does not satisfy
// IRHolder and the IR/AST are eligible for garbage collection as soon
// as Compile returns.
type IRHolder interface {
	linker.File
	IR() *ir.File
}

// irHoldingFile wraps a linker.File with a strong reference to the
// experimental [*ir.File] so callers that asked for RetainASTs can
// recover it via the [IRHolder] interface.
type irHoldingFile struct {
	linker.File
	ir *ir.File
}

// IR returns the experimental IR file that backs this linker.File.
func (h *irHoldingFile) IR() *ir.File { return h.ir }

// fdpOptionsFor maps an `exphook.Args.SourceInfoMode` (mirror of
// `protocompile.SourceInfoMode`) to the corresponding set of
// [fdp.DescriptorOption]s.
//
// The mode is a bit-flag enum; the experimental fdp layer exposes two
// flags: IncludeSourceCodeInfo (whether to emit SourceCodeInfo at all)
// and GenerateExtraOptionLocations (whether to emit additional
// locations for fields inside message-literal option values).
// SourceInfoExtraComments (mode 2) has no direct fdp equivalent today
// and is silently treated the same as SourceInfoStandard — the
// experimental IR emits its own comment-tracking shape, and refining
// the mapping is a follow-up.
func fdpOptionsFor(mode int) []fdp.DescriptorOption {
	if mode == exphook.SourceInfoNone {
		return nil
	}
	opts := []fdp.DescriptorOption{fdp.IncludeSourceCodeInfo(true)}
	if mode&exphook.SourceInfoExtraOptionLocations != 0 {
		opts = append(opts, fdp.GenerateExtraOptionLocations(true))
	}
	return opts
}

// irFileToLinkerFile converts an *ir.File into a linker.File by
// generating its FileDescriptorProto, building a protoreflect
// descriptor, and wrapping it via linker.NewFileRecursive.
//
// fdpOpts are forwarded to the fdp generator on both the top file and
// each transitive dependency. The same opts are passed downward so a
// caller asking for source-code info on the top file also gets it on
// the imports — this matches what the legacy compiler does (sourceinfo
// is keyed off the Compiler-level flag, not per-file).
func irFileToLinkerFile(file *ir.File, fdpOpts []fdp.DescriptorOption) (linker.File, error) {
	if file == nil {
		return nil, errors.New("nil ir.File")
	}

	registry, topProto, err := buildExperimentalRegistry(file, fdpOpts)
	if err != nil {
		return nil, err
	}

	// Round-trip the top-level FDP through the dynamicpb resolver so
	// extensions defined in this file (or its transitive imports)
	// surface as typed fields rather than unknown wire bytes.
	rawBytes, err := fdp.DescriptorProtoBytes(file, fdpOpts...)
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
// top-level file's freshly generated FileDescriptorProto. The same
// fdpOpts are passed to every fdp.DescriptorProto call so dependencies
// pick up sourceinfo when the top-level file does.
func buildExperimentalRegistry(top *ir.File, fdpOpts []fdp.DescriptorOption) (*protoregistry.Files, *descriptorpb.FileDescriptorProto, error) {
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

		dep, err := fdp.DescriptorProto(irFile, fdpOpts...)
		if err != nil || dep == nil {
			return err
		}

		// The fdp generator parks every options message under
		// `descriptorpb.X.SetUnknown(...)`. That keeps the wire bytes
		// intact but leaves the typed fields (e.g.
		// `EnumOptions.AllowAlias`) zero, which trips protodesc's
		// validation passes — most notably the enum-value uniqueness
		// check, which only allows duplicates when allow_alias is true.
		// Re-serialise and unmarshal via the registry-aware resolver
		// here so the typed fields surface before protodesc sees them.
		// This mirrors the round-trip irFileToLinkerFile already does
		// for the top-level descriptor; without it, files like
		// internal/testdata/options/test.proto (`TestEnum` with two
		// `= 0` values guarded by `option allow_alias = true`) refuse
		// to register and the whole compile fails.
		rawDepBytes, err := fdp.DescriptorProtoBytes(irFile, fdpOpts...)
		if err == nil {
			typedDep := new(descriptorpb.FileDescriptorProto)
			depResolver := dynamicpb.NewTypes(files)
			if err := (protoUnmarshal{resolver: depResolver}).do(rawDepBytes, typedDep); err == nil {
				dep = typedDep
			}
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
// source.Opener. The user's resolver is consulted first so that
// project-supplied overrides of well-known types (e.g. a custom
// `google/protobuf/descriptor.proto` that adds extra fields to
// `EnumOptions`) win; [source.WKTs] is the fallback for paths the
// resolver doesn't know. This matches the legacy
// `WithStandardImports` semantics, where the wrapped resolver is
// tried first and the baked-in well-knowns are the fallback.
//
// When the user-supplied descriptor.proto is a *partial* vendored
// override (declares only some of the descriptor types), the IR's
// [resolveBuiltins] consults a session-scoped baked-in fallback to
// materialise missing builtin symbols as stubs in the user's file —
// see `experimental/ir/builtins_copy.go`.
//
// Result types other than Source are surfaced as not-found for now.
func experimentalOpener(resolver exphook.Resolver) source.Opener {
	wkts := source.WKTs()
	if resolver == nil {
		return wkts
	}
	return &source.Openers{&resolverOpener{resolver: resolver}, wkts}
}

type resolverOpener struct {
	resolver exphook.Resolver
}

func (r *resolverOpener) Open(path string) (*source.File, error) {
	src, err := r.resolver.OpenSource(path)
	if err != nil {
		return nil, err
	}
	if src == nil {
		return nil, fs.ErrNotExist
	}
	data, err := io.ReadAll(src)
	if err != nil {
		return nil, err
	}
	return source.NewFile(path, string(data)), nil
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
