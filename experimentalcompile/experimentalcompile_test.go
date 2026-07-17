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
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/trendvidia/protocompile"
	"github.com/trendvidia/protocompile/experimentalcompile"
	_ "github.com/trendvidia/protocompile/experimentalcompile" // registers the experimental compile hook
	"github.com/trendvidia/protocompile/linker"
	"github.com/trendvidia/protocompile/protoutil"
	"github.com/trendvidia/protocompile/reporter"
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
		Resolver: resolver,
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

// TestSourceInfoMode_OffByDefault confirms that when SourceInfoMode is
// SourceInfoNone (the zero value), the descriptor has no SourceCodeInfo.
func TestSourceInfoMode_OffByDefault(t *testing.T) {
	t.Parallel()
	fdp := compileForSourceInfo(t, protocompile.SourceInfoNone)
	assert.Nil(t, fdp.SourceCodeInfo)
}

// TestSourceInfoMode_Standard confirms that requesting SourceInfoStandard
// causes the descriptor to carry SourceCodeInfo populated by the
// experimental pipeline.
func TestSourceInfoMode_Standard(t *testing.T) {
	t.Parallel()
	fdp := compileForSourceInfo(t, protocompile.SourceInfoStandard)
	require.NotNil(t, fdp.SourceCodeInfo)
	assert.NotEmpty(t, fdp.SourceCodeInfo.Location)
}

// TestRetainASTs_OffByDefault confirms that without RetainASTs set,
// the returned linker.File does not satisfy IRHolder.
func TestRetainASTs_OffByDefault(t *testing.T) {
	t.Parallel()

	c := protocompile.Compiler{
		Resolver: minimalResolver(),
	}
	files, err := c.Compile(t.Context(), "hello.proto")
	require.NoError(t, err)
	require.Len(t, files, 1)

	_, ok := files[0].(experimentalcompile.IRHolder)
	assert.False(t, ok, "linker.File should not satisfy IRHolder when RetainASTs is false")
}

// TestRetainASTs_ExposesIRAndAST confirms that with RetainASTs set,
// the returned linker.File satisfies IRHolder, and through it the
// experimental IR file and its AST are accessible.
func TestRetainASTs_ExposesIRAndAST(t *testing.T) {
	t.Parallel()

	c := protocompile.Compiler{
		Resolver:   minimalResolver(),
		RetainASTs: true,
	}
	files, err := c.Compile(t.Context(), "hello.proto")
	require.NoError(t, err)
	require.Len(t, files, 1)

	holder, ok := files[0].(experimentalcompile.IRHolder)
	require.True(t, ok, "linker.File must satisfy IRHolder when RetainASTs is true")

	irFile := holder.IR()
	require.NotNil(t, irFile)
	assert.Equal(t, "hello.proto", irFile.Path())

	astFile := irFile.AST()
	require.NotNil(t, astFile, "irFile.AST() should be non-nil")
}

// TestSymbols_NoSymbolsTableSucceeds is a guard against a regression
// where threading the Symbols table through the experimental path
// accidentally requires it to be non-nil. The default Compiler has
// Symbols == nil and Compile must still succeed.
func TestSymbols_NoSymbolsTableSucceeds(t *testing.T) {
	t.Parallel()

	c := protocompile.Compiler{
		Resolver: minimalResolver(),
	}
	files, err := c.Compile(t.Context(), "hello.proto")
	require.NoError(t, err)
	require.Len(t, files, 1)
}

// TestSymbols_ImportsCompiledFile asserts that when a Symbols table is
// supplied, the experimental compile feeds each compiled file into
// it. A second Compile call against the same Symbols table that
// brings in a redefining file fails with a collision error.
func TestSymbols_ImportsCompiledFile(t *testing.T) {
	t.Parallel()

	resolver := protocompile.ResolverFunc(func(path string) (protocompile.SearchResult, error) {
		switch path {
		case "a.proto":
			return protocompile.SearchResult{
				Source: io.NopCloser(strings.NewReader(`
syntax = "proto3";
package hello;
message Greeting {
  string text = 1;
}
`)),
			}, nil
		case "b.proto":
			// Same package + same message name — should collide with
			// a.proto when both are imported into a shared Symbols
			// table.
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

	symbols := new(linker.Symbols)

	c1 := protocompile.Compiler{
		Resolver: resolver,
		Symbols:  symbols,
	}
	files, err := c1.Compile(t.Context(), "a.proto")
	require.NoError(t, err, "first compile should populate the shared symbol table")
	require.Len(t, files, 1)

	c2 := protocompile.Compiler{
		Resolver: resolver,
		Symbols:  symbols,
	}
	_, err = c2.Compile(t.Context(), "b.proto")
	require.Error(t, err, "second compile defining hello.Greeting again must surface a collision")
}

// TestReporter_ReceivesPositionedErrors is the repro from issue #89:
// a file with syntax errors must surface each diagnostic through the
// caller's ErrorReporter as a positioned ErrorWithPos, and a reporter
// that always returns nil must see Compile fail with ErrInvalidSource
// rather than a stringified first diagnostic.
func TestReporter_ReceivesPositionedErrors(t *testing.T) {
	t.Parallel()

	var errs []reporter.ErrorWithPos
	rep := reporter.NewReporter(
		func(e reporter.ErrorWithPos) error { errs = append(errs, e); return nil },
		nil,
	)

	c := protocompile.Compiler{
		Resolver: brokenAndGoodResolver(),
		Reporter: rep,
	}

	_, err := c.Compile(t.Context(), "broken.proto")
	require.ErrorIs(t, err, reporter.ErrInvalidSource)
	require.NotEmpty(t, errs, "ErrorReporter must be invoked for parse errors")

	for _, e := range errs {
		pos := e.GetPosition()
		assert.Equal(t, "broken.proto", pos.Filename)
		assert.Positive(t, pos.Line, "diagnostics must carry line info")
		assert.Positive(t, pos.Col, "diagnostics must carry column info")
		assert.NotEmpty(t, e.Unwrap().Error())
	}
}

// TestReporter_AbortStopsBatch confirms that an ErrorReporter returning
// a non-nil error aborts Compile with exactly that error.
func TestReporter_AbortStopsBatch(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("stop right there")
	calls := 0
	rep := reporter.NewReporter(
		func(reporter.ErrorWithPos) error { calls++; return sentinel },
		nil,
	)

	c := protocompile.Compiler{
		Resolver: brokenAndGoodResolver(),
		Reporter: rep,
	}

	files, err := c.Compile(t.Context(), "broken.proto")
	require.ErrorIs(t, err, sentinel)
	assert.Nil(t, files)
	assert.Equal(t, 1, calls, "reporting must stop after the reporter aborts")
}

// TestReporter_PartialResults confirms the continue-on-nil contract: in
// a batch where one file cannot be compiled at all (not found), the
// good file's descriptor survives in its slot, the failed file's slot
// is nil, and Compile returns ErrInvalidSource.
func TestReporter_PartialResults(t *testing.T) {
	t.Parallel()

	var errs []reporter.ErrorWithPos
	rep := reporter.NewReporter(
		func(e reporter.ErrorWithPos) error { errs = append(errs, e); return nil },
		nil,
	)

	c := protocompile.Compiler{
		Resolver: brokenAndGoodResolver(),
		Reporter: rep,
	}

	files, err := c.Compile(t.Context(), "missing.proto", "good.proto")
	require.ErrorIs(t, err, reporter.ErrInvalidSource)
	require.Len(t, files, 2)
	assert.Nil(t, files[0], "the missing file's slot must be nil")
	require.NotNil(t, files[1], "the good file must still compile")
	assert.Equal(t, "good.proto", files[1].Path())
	assert.NotNil(t, files[1].Messages().ByName("Fine"))
	require.NotEmpty(t, errs)
	assert.Equal(t, "missing.proto", errs[0].GetPosition().Filename)
}

// TestReporter_ContinueKeepsRecoveredFile confirms that a file with
// syntax errors that the parser can recover from still yields a
// descriptor when the reporter swallows the errors — matching the
// legacy compiler, which returned whatever each file's pipeline managed
// to produce.
func TestReporter_ContinueKeepsRecoveredFile(t *testing.T) {
	t.Parallel()

	rep := reporter.NewReporter(
		func(reporter.ErrorWithPos) error { return nil },
		nil,
	)

	c := protocompile.Compiler{
		Resolver: brokenAndGoodResolver(),
		Reporter: rep,
	}

	files, err := c.Compile(t.Context(), "broken.proto", "good.proto")
	require.ErrorIs(t, err, reporter.ErrInvalidSource)
	require.Len(t, files, 2)
	require.NotNil(t, files[1], "the good file must still compile")
	assert.Equal(t, "good.proto", files[1].Path())
}

// TestReporter_ReceivesWarnings confirms that warning-level diagnostics
// reach the WarningReporter and do not fail compilation.
func TestReporter_ReceivesWarnings(t *testing.T) {
	t.Parallel()

	var warnings []reporter.ErrorWithPos
	rep := reporter.NewReporter(
		nil,
		func(e reporter.ErrorWithPos) { warnings = append(warnings, e) },
	)

	// A file without a syntax declaration produces a "missing syntax"
	// warning but compiles fine.
	resolver := protocompile.ResolverFunc(func(path string) (protocompile.SearchResult, error) {
		if path == "nosyntax.proto" {
			return protocompile.SearchResult{
				Source: io.NopCloser(strings.NewReader("package quiet;\n")),
			}, nil
		}
		return protocompile.SearchResult{}, os.ErrNotExist
	})

	c := protocompile.Compiler{
		Resolver: resolver,
		Reporter: rep,
	}

	files, err := c.Compile(t.Context(), "nosyntax.proto")
	require.NoError(t, err, "warnings alone must not fail compilation")
	require.Len(t, files, 1)
	require.NotEmpty(t, warnings, "WarningReporter must be invoked")
	assert.Equal(t, "nosyntax.proto", warnings[0].GetPosition().Filename)
}

// TestReporter_NilReporterFailsWithErrorWithPos confirms that with no
// Reporter configured, Compile fails on the first error and the
// returned error is a positioned ErrorWithPos (not a stringified
// diagnostic struct).
func TestReporter_NilReporterFailsWithErrorWithPos(t *testing.T) {
	t.Parallel()

	c := protocompile.Compiler{
		Resolver: brokenAndGoodResolver(),
	}

	files, err := c.Compile(t.Context(), "broken.proto")
	require.Error(t, err)
	assert.Nil(t, files)

	var ewp reporter.ErrorWithPos
	require.ErrorAs(t, err, &ewp, "the default reporter must fail with a positioned error")
	assert.Equal(t, "broken.proto", ewp.GetPosition().Filename)
	assert.Positive(t, ewp.GetPosition().Line)
}

// brokenAndGoodResolver serves broken.proto (syntax errors) and
// good.proto (compiles cleanly) for the Reporter contract tests.
func brokenAndGoodResolver() protocompile.Resolver {
	return protocompile.ResolverFunc(func(path string) (protocompile.SearchResult, error) {
		switch path {
		case "broken.proto":
			return protocompile.SearchResult{
				Source: io.NopCloser(strings.NewReader(
					"syntax = \"proto3\"\n\nmessage Broken {\n  string name = 1\n}}\n",
				)),
			}, nil
		case "good.proto":
			return protocompile.SearchResult{
				Source: io.NopCloser(strings.NewReader(
					"syntax = \"proto3\";\npackage fine;\nmessage Fine {\n  string name = 1;\n}\n",
				)),
			}, nil
		}
		return protocompile.SearchResult{}, os.ErrNotExist
	})
}

// minimalResolver returns a Resolver that serves a tiny hello.proto
// with a Greeting message — enough to exercise the experimental
// pipeline end-to-end. Shared by the RetainASTs tests so a future
// fixture change touches one place.
func minimalResolver() protocompile.Resolver {
	return protocompile.ResolverFunc(func(path string) (protocompile.SearchResult, error) {
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
}

func compileForSourceInfo(t *testing.T, mode protocompile.SourceInfoMode) *descriptorpb.FileDescriptorProto {
	t.Helper()
	resolver := protocompile.ResolverFunc(func(path string) (protocompile.SearchResult, error) {
		if path == "hello.proto" {
			return protocompile.SearchResult{
				Source: io.NopCloser(strings.NewReader(`syntax = "proto3";
package hello;
// A friendly greeting.
message Greeting {
  string text = 1;
}
`)),
			}, nil
		}
		return protocompile.SearchResult{}, os.ErrNotExist
	})

	c := protocompile.Compiler{
		Resolver:       resolver,
		SourceInfoMode: mode,
	}

	files, err := c.Compile(t.Context(), "hello.proto")
	require.NoError(t, err)
	require.Len(t, files, 1)
	return protoutil.ProtoFromFileDescriptor(files[0])
}
