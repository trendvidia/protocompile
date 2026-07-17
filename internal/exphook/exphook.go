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

// Package exphook is the minimal contract between the root
// `protocompile` package and the `experimentalcompile` package. It
// exists solely to break the import cycle that would otherwise form
// when `protocompile` blank-imports `experimentalcompile` to make
// experimental the default pipeline:
//
//	protocompile  ──→  experimentalcompile  ──→  protocompile  (cycle)
//
// Both ends now import this small package instead of each other:
//
//	protocompile  ──→  internal/exphook  ←──  experimentalcompile
//
// The contract here is intentionally minimal — just the fields the
// experimental pipeline reads from a [protocompile.Compiler], plus a
// tiny resolver interface that takes the place of `protocompile.Resolver`
// for the experimental side. The root `protocompile` package supplies
// an adapter that fills [Args] from a `*Compiler`.
//
// This package has no dependencies on `protocompile` or the
// experimental subpackages other than `linker` and `reporter`, both of
// which are leaf packages with no protocompile dependency of their own.
package exphook

import (
	"context"
	"io"

	"github.com/trendvidia/protocompile/linker"
	"github.com/trendvidia/protocompile/reporter"
)

// Resolver is the slice of `protocompile.Resolver` the experimental
// pipeline needs: open a file's source by path. Returning a nil reader
// (with no error) signals that the resolver knows the file exists in
// some other form (e.g. as a `Desc`) that the experimental pipeline
// does not yet support; the caller should treat this as not-found and
// either error out or fall back as appropriate.
type Resolver interface {
	OpenSource(path string) (io.Reader, error)
}

// Args is the full set of compiler-level inputs the experimental
// pipeline reads from. It is filled by `protocompile.Compile` from a
// `*Compiler` before dispatching to the registered [CompileFunc].
type Args struct {
	// Resolver opens source for files the experimental pipeline needs
	// to compile. Required.
	Resolver Resolver

	// Symbols is the shared symbol table; the experimental pipeline
	// feeds each compiled file into it so cross-Compile collisions
	// surface as errors. Optional.
	Symbols *linker.Symbols

	// Reporter receives every error and warning diagnostic the pipeline
	// produces (including symbol-table import collisions), as
	// positioned [reporter.ErrorWithPos] values. Optional; when nil the
	// first error fails the batch and warnings are dropped.
	Reporter reporter.Reporter

	// SourceInfoMode is the same integer value as
	// `protocompile.SourceInfoMode`. Defined here as a plain int so
	// this package does not have to import `protocompile`. Constants
	// are mirrored at the bottom of this file.
	SourceInfoMode int

	// RetainASTs requests that the returned [linker.File]s also
	// satisfy the IRHolder interface in `experimentalcompile`.
	RetainASTs bool
}

// CompileFunc is the registered implementation of the experimental
// compile entry point. The signature mirrors the experimentalcompile
// package's own `Compile` function (which is what gets registered).
type CompileFunc func(ctx context.Context, args Args, files []string) (linker.Files, error)

// SourceInfoMode constants. These mirror the same-named constants on
// the root protocompile package; redefined here so this contract
// package can stay free of a protocompile dependency. Callers convert
// between the two by an int cast.
const (
	SourceInfoNone                 = 0
	SourceInfoStandard             = 1
	SourceInfoExtraComments        = 2
	SourceInfoExtraOptionLocations = 4
)

var registered CompileFunc

// Register installs the experimental compile implementation. Called
// from `experimentalcompile.init` so a blank import of that package
// activates the experimental pipeline. A nil function deregisters,
// which is useful in tests.
func Register(fn CompileFunc) {
	registered = fn
}

// Get returns the registered experimental compile implementation, or
// nil if none has been registered. Consulted by `protocompile.Compile`
// during dispatch.
func Get() CompileFunc {
	return registered
}
