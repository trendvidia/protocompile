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

package ir_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trendvidia/protocompile/ast"
	"github.com/trendvidia/protocompile/incremental"
	"github.com/trendvidia/protocompile/incremental/queries"
	"github.com/trendvidia/protocompile/ir"
	"github.com/trendvidia/protocompile/report"
	"github.com/trendvidia/protocompile/seq"
	"github.com/trendvidia/protocompile/source"
)

// TestAnnotationSymbol verifies B1 wiring: an `annotation` declaration
// shows up as an [ir.Annotation] in [(*ir.File).Annotations()], gets
// registered under [ir.SymbolKindAnnotation] in the file's exported
// symbol table, parameter list is materialised in source order, and
// [Symbol.AsAnnotation] round-trips back to the same [ir.Annotation]
// instance.
func TestAnnotationSymbol(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

annotation required;
annotation description(text: string);
annotation example(value: any);
`

	opener := source.NewMap(map[string]*source.File{
		"x.proto": source.NewFile("x.proto", src),
	})
	allOpeners := &source.Openers{opener, source.WKTs()}

	exec := incremental.New()
	sess := new(ir.Session)
	results, _, err := incremental.Run(t.Context(), exec, queries.IR{
		Opener:  allOpeners,
		Session: sess,
		Path:    "x.proto",
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.NoError(t, results[0].Fatal)
	file := results[0].Value
	require.NotNil(t, file)

	annsSeq := file.Annotations()
	require.Equal(t, 3, annsSeq.Len(), "annotation declarations should round-trip from AST to IR")

	var anns []ir.Annotation
	for a := range seq.Values(annsSeq) {
		anns = append(anns, a)
	}

	expectedNames := []string{"required", "description", "example"}
	expectedFQNs := []string{"test.required", "test.description", "test.example"}
	expectedParams := [][]string{nil, {"text"}, {"value"}}

	for i, a := range anns {
		assert.Equal(t, expectedNames[i], a.Name(), "annotation %d name", i)
		assert.Equal(t, expectedFQNs[i], string(a.FullName()), "annotation %d FQN", i)
		assert.False(t, a.AST().IsZero(), "annotation %d AST link", i)
		assert.NotZero(t, a.InternedFullName(), "annotation %d interned FQN", i)

		paramSeq := a.Params()
		assert.Equal(t, len(expectedParams[i]), paramSeq.Len(), "annotation %d param count", i)
		var got []string
		for p := range seq.Values(paramSeq) {
			got = append(got, p.Name())
			assert.False(t, p.AST().IsZero(), "annotation %d param %q AST link", i, p.Name())
			assert.Equal(t, a, p.Annotation(), "annotation %d param %q parent backref", i, p.Name())
		}
		assert.Equal(t, expectedParams[i], got, "annotation %d param names", i)
	}

	// Each declaration must appear in the file's symbol table under
	// SymbolKindAnnotation. Use the file's public FindSymbol — same
	// lookup path use-site resolution will use in Phase B2.
	for i, fqn := range expectedFQNs {
		sym := file.FindSymbol(ir.FullName(fqn))
		require.False(t, sym.IsZero(), "missing symbol for %s", fqn)
		assert.Equal(t, ir.SymbolKindAnnotation, sym.Kind(), "symbol kind for %s", fqn)
		assert.Equal(t, anns[i], sym.AsAnnotation(), "AsAnnotation round-trip for %s", fqn)
	}
}

// TestAnnotationUseResolution exercises B2: every `@name(args)` use
// site on every IR carrier gets materialised as an [ir.AnnotationUse]
// and resolved against the symbol table. Hits the four canonical
// carrier kinds (message/enum/service/method/field), the trailing
// form on `annotation` declarations themselves, qualified names, and
// the unresolved-name diagnostic path.
func TestAnnotationUseResolution(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

annotation deprecated;
annotation authored(by: string);
annotation reviewed;
annotation since(when: string);
annotation core;

@deprecated
message M {
  string field_a = 1 @authored("alice");
}

@authored("alice")
@reviewed
enum E {
  E_UNSET = 0;
  E_ONE = 1;
}

@since("2026-06-11")
service S {
  rpc Ping(M) returns (M);
}

annotation derived @core;
`

	opener := source.NewMap(map[string]*source.File{
		"x.proto": source.NewFile("x.proto", src),
	})
	allOpeners := &source.Openers{opener, source.WKTs()}

	exec := incremental.New()
	sess := new(ir.Session)
	results, _, err := incremental.Run(t.Context(), exec, queries.IR{
		Opener:  allOpeners,
		Session: sess,
		Path:    "x.proto",
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.NoError(t, results[0].Fatal)
	file := results[0].Value
	require.NotNil(t, file)

	// Helper: collect resolved Annotation.FullName for a carrier's
	// annotation use sites. Empty string for unresolved.
	collectTargets := func(uses seq.Indexer[ir.AnnotationUse]) []string {
		var out []string
		for u := range seq.Values(uses) {
			tgt := u.Target()
			if tgt.IsZero() {
				out = append(out, "")
				continue
			}
			out = append(out, string(tgt.FullName()))
		}
		return out
	}

	// Walk every IR type and check its annotation set + nested members.
	var seenMessageM, seenEnumE bool
	for ty := range seq.Values(file.AllTypes()) {
		switch ty.Name() {
		case "M":
			seenMessageM = true
			assert.Equal(t, []string{"test.deprecated"}, collectTargets(ty.Annotations()),
				"message M leading annotation")
			for f := range seq.Values(ty.Members()) {
				if f.Name() == "field_a" {
					assert.Equal(t, []string{"test.authored"}, collectTargets(f.Annotations()),
						"M.field_a trailing annotation")
				}
			}
		case "E":
			seenEnumE = true
			assert.Equal(t, []string{"test.authored", "test.reviewed"}, collectTargets(ty.Annotations()),
				"enum E leading annotations")
		}
	}
	assert.True(t, seenMessageM, "expected to visit message M")
	assert.True(t, seenEnumE, "expected to visit enum E")

	var seenService bool
	for svc := range seq.Values(file.Services()) {
		if svc.Name() != "S" {
			continue
		}
		seenService = true
		assert.Equal(t, []string{"test.since"}, collectTargets(svc.Annotations()),
			"service S leading annotation")
	}
	assert.True(t, seenService, "expected to visit service S")

	// `annotation derived @core;` — trailing annotation on an
	// annotation declaration, exercising the B1 carrier from B2.
	var seenDerived bool
	for ann := range seq.Values(file.Annotations()) {
		if ann.Name() != "derived" {
			continue
		}
		seenDerived = true
		assert.Equal(t, []string{"test.core"}, collectTargets(ann.Annotations()),
			"annotation derived trailing @core")
	}
	assert.True(t, seenDerived, "expected to visit annotation derived")
}

// TestAnnotationUseUnresolved verifies that an unknown annotation
// name produces a diagnostic and a zero [AnnotationUse.Target].
// The AnnotationUse itself is still recorded so later passes don't
// silently drop entries.
func TestAnnotationUseUnresolved(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

@undeclared
message M {}
`

	opener := source.NewMap(map[string]*source.File{
		"x.proto": source.NewFile("x.proto", src),
	})
	allOpeners := &source.Openers{opener, source.WKTs()}

	exec := incremental.New()
	sess := new(ir.Session)
	results, rep, err := incremental.Run(t.Context(), exec, queries.IR{
		Opener:  allOpeners,
		Session: sess,
		Path:    "x.proto",
	})
	require.NoError(t, err)
	require.NotNil(t, rep)

	// The compile still produces an IR; only the use-site target is zero.
	require.Len(t, results, 1)
	require.NotNil(t, results[0].Value)

	var sawDiag bool
	for _, d := range rep.Diagnostics {
		if strings.Contains(d.Message(), "undeclared") {
			sawDiag = true
			break
		}
	}
	assert.True(t, sawDiag, "expected diagnostic mentioning `undeclared`")

	for ty := range seq.Values(results[0].Value.AllTypes()) {
		if ty.Name() != "M" {
			continue
		}
		uses := ty.Annotations()
		require.Equal(t, 1, uses.Len(), "use site still recorded")
		assert.True(t, uses.At(0).Target().IsZero(), "unresolved target should be zero")
	}
}

// compileForAnnotationTest is a small helper that drives the IR
// compile through `queries.IR` and returns the resulting file plus
// the diagnostic report.
func compileForAnnotationTest(t *testing.T, src string) (*ir.File, *report.Report) {
	t.Helper()
	opener := source.NewMap(map[string]*source.File{
		"x.proto": source.NewFile("x.proto", src),
	})
	allOpeners := &source.Openers{opener, source.WKTs()}

	exec := incremental.New()
	sess := new(ir.Session)
	results, rep, err := incremental.Run(t.Context(), exec, queries.IR{
		Opener:  allOpeners,
		Session: sess,
		Path:    "x.proto",
	})
	require.NoError(t, err)
	require.NotNil(t, rep)
	require.Len(t, results, 1)
	require.NotNil(t, results[0].Value)
	return results[0].Value, rep
}

// TestAnnotationParamTypeClassification verifies B3's parameter
// type resolution: each `name: type` in an annotation parameter list
// gets classified as scalar / `expression` / `any` / user type, and
// the corresponding accessor returns the right thing.
func TestAnnotationParamTypeClassification(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

message Email {
  string addr = 1;
}

annotation validate(rule: expression, code: string = "", note: any);
annotation deprecated_since(year: int32);
annotation contact(via: Email);
`

	file, rep := compileForAnnotationTest(t, src)
	// No diagnostics for valid types.
	for _, d := range rep.Diagnostics {
		if isError(d) {
			t.Errorf("unexpected diagnostic: %s", d.Message())
		}
	}

	byName := map[string]ir.Annotation{}
	for a := range seq.Values(file.Annotations()) {
		byName[a.Name()] = a
	}

	// validate(rule: expression, code: string = "", note: any)
	v := byName["validate"]
	require.False(t, v.IsZero(), "missing annotation `validate`")
	require.Equal(t, 3, v.Params().Len())
	p0 := v.Params().At(0)
	assert.True(t, p0.IsExpression(), "rule should be expression")
	assert.False(t, p0.IsScalar())
	assert.False(t, p0.IsAny())
	assert.Equal(t, "expression", p0.TypeName())

	p1 := v.Params().At(1)
	assert.True(t, p1.IsScalar(), "code should be scalar")
	assert.Equal(t, "string", p1.TypeName())

	p2 := v.Params().At(2)
	assert.True(t, p2.IsAny(), "note should be any")
	assert.Equal(t, "any", p2.TypeName())

	// deprecated_since(year: int32)
	d := byName["deprecated_since"]
	require.False(t, d.IsZero())
	p := d.Params().At(0)
	assert.True(t, p.IsScalar())
	assert.Equal(t, "int32", p.TypeName())

	// contact(via: Email) — user type
	c := byName["contact"]
	require.False(t, c.IsZero())
	pv := c.Params().At(0)
	assert.False(t, pv.IsScalar())
	assert.False(t, pv.IsExpression())
	assert.False(t, pv.IsAny())
	userTy := pv.UserType()
	require.False(t, userTy.IsZero(), "user type should resolve")
	assert.Equal(t, "test.Email", string(userTy.FullName()))
}

// TestAnnotationArgTypeMismatch checks the per-arg type-check
// diagnostics: each predeclared scalar's expected literal kind is
// enforced.
func TestAnnotationArgTypeMismatch(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

annotation needs_string(s: string);
annotation needs_int(n: int32);
annotation needs_bool(b: bool);

@needs_string(42)
message MismatchA {}

@needs_int("hello")
message MismatchB {}

@needs_bool(123)
message MismatchC {}
`

	_, rep := compileForAnnotationTest(t, src)

	// Tally one mismatch error per fixture site.
	wantMessages := []string{
		"argument \"s\" for `test.needs_string` expects string, got number literal",
		"argument \"n\" for `test.needs_int` expects int32, got string literal",
		"argument \"b\" for `test.needs_bool` expects bool, got number literal",
	}
	for _, want := range wantMessages {
		var found bool
		for _, d := range rep.Diagnostics {
			if strings.Contains(d.Message(), want) {
				found = true
				break
			}
		}
		assert.True(t, found, "expected diagnostic: %s", want)
	}
}

// TestAnnotationArgTooMany verifies the arity check: passing more
// args than the parameter list declares is an error.
func TestAnnotationArgTooMany(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

annotation tag(name: string);

@tag("alpha", "beta")
message M {}
`

	_, rep := compileForAnnotationTest(t, src)
	var found bool
	for _, d := range rep.Diagnostics {
		if strings.Contains(d.Message(), "too many arguments for `test.tag`") &&
			strings.Contains(d.Message(), "got 2") {
			found = true
			break
		}
	}
	assert.True(t, found, "expected arity diagnostic")
}

// TestAnnotationArgMissingRequired verifies that a use site which
// leaves a parameter without a default unbound is diagnosed with the
// missing parameter's name — for the empty-parens form, the bare
// `@name` form, and a partial named argument list alike.
func TestAnnotationArgMissingRequired(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

annotation description(text: string);
annotation pair(a: string, b: string);
annotation labeled(text: string, level: int32 = 1);

@description()
message EmptyParens {}

@description
message Bare {}

@pair(a = "x")
message Partial {}

@labeled("x")
message DefaultCovers {}
`

	_, rep := compileForAnnotationTest(t, src)

	assert.True(t,
		hasErrorContaining(rep, "missing required argument \"text\" for `test.description`"),
		"expected missing-argument diagnostic for `@description()` and `@description`")
	assert.True(t,
		hasErrorContaining(rep, "missing required argument \"b\" for `test.pair`"),
		"expected missing-argument diagnostic for `@pair(a = \"x\")`")

	// Both the empty-parens and the bare use site must be diagnosed.
	var missingText int
	for _, d := range rep.Diagnostics {
		if isError(d) &&
			strings.Contains(d.Message(), "missing required argument \"text\" for `test.description`") {
			missingText++
		}
	}
	assert.Equal(t, 2, missingText, "want one diagnostic per `@description` use site")

	// A parameter with a default is not required, and a bound
	// required parameter is not re-reported.
	assert.False(t,
		hasErrorContaining(rep, "missing required argument", "test.labeled"),
		"`level` has a default; `@labeled(\"x\")` must not be diagnosed")
	assert.False(t,
		hasErrorContaining(rep, "missing required argument \"a\""),
		"`a` is bound by `@pair(a = \"x\")`")
}

// TestAnnotationEnumArgAccepted verifies that an enum-typed user-
// type parameter accepts an identifier-path argument that resolves
// to an enum value of the matching enum, with no diagnostics.
func TestAnnotationEnumArgAccepted(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

enum Visibility {
  VIS_UNSET = 0;
  PUBLIC = 1;
  PRIVATE = 2;
}

annotation scope(via: Visibility);

@scope(PUBLIC)
message M {}
`

	_, rep := compileForAnnotationTest(t, src)
	for _, d := range rep.Diagnostics {
		if isError(d) {
			t.Errorf("unexpected diagnostic: %s", d.Message())
		}
	}
}

// TestAnnotationEnumArgWrongEnum verifies that supplying a value of
// a *different* enum produces a diagnostic that names both enums.
func TestAnnotationEnumArgWrongEnum(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

enum Visibility { VIS_UNSET = 0; PUBLIC = 1; }
enum Color      { COL_UNSET = 0; RED    = 1; }

annotation scope(via: Visibility);

@scope(RED)
message M {}
`

	_, rep := compileForAnnotationTest(t, src)
	var saw bool
	for _, d := range rep.Diagnostics {
		if strings.Contains(d.Message(), "test.scope") &&
			strings.Contains(d.Message(), "test.Visibility") &&
			strings.Contains(d.Message(), "test.Color") {
			saw = true
			break
		}
	}
	assert.True(t, saw, "expected wrong-enum diagnostic mentioning both Visibility and Color")
}

// TestAnnotationEnumArgUndefined verifies that supplying an
// undefined identifier produces the standard "cannot find" symbol-
// resolution diagnostic.
func TestAnnotationEnumArgUndefined(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

enum Visibility { VIS_UNSET = 0; PUBLIC = 1; }

annotation scope(via: Visibility);

@scope(NOT_A_VALUE)
message M {}
`

	_, rep := compileForAnnotationTest(t, src)
	var saw bool
	for _, d := range rep.Diagnostics {
		if strings.Contains(d.Message(), "cannot find `NOT_A_VALUE`") {
			saw = true
			break
		}
	}
	assert.True(t, saw, "expected unresolved-symbol diagnostic")
}

// TestAnnotationEnumArgLiteral verifies that supplying a literal
// (number or string) to an enum-typed parameter produces a
// diagnostic that names the expected enum type.
func TestAnnotationEnumArgLiteral(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

enum Visibility { VIS_UNSET = 0; PUBLIC = 1; }

annotation scope(via: Visibility);

@scope(42)
message M {}
`

	_, rep := compileForAnnotationTest(t, src)
	var saw bool
	for _, d := range rep.Diagnostics {
		if strings.Contains(d.Message(), "test.scope") &&
			strings.Contains(d.Message(), "test.Visibility") &&
			strings.Contains(d.Message(), "literal") {
			saw = true
			break
		}
	}
	assert.True(t, saw, "expected literal-vs-enum diagnostic")
}

// TestAnnotationParamDefaultTypeCheck verifies that default-value
// expressions on annotation parameters are type-checked against the
// parameter's declared type, the same way use-site arguments are.
func TestAnnotationParamDefaultTypeCheck(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

// Well-typed defaults should compile cleanly.
annotation ok(name: string = "anonymous", count: int32 = 0, flag: bool = false);

// Bad defaults should be diagnosed.
annotation bad(name: string = 42);
`

	_, rep := compileForAnnotationTest(t, src)

	// `ok` produces no diagnostics.
	for _, d := range rep.Diagnostics {
		if isError(d) && strings.Contains(d.Message(), "test.ok") {
			t.Errorf("unexpected diagnostic for `test.ok`: %s", d.Message())
		}
	}

	// `bad` produces a mismatch diagnostic mentioning the offending param.
	var saw bool
	for _, d := range rep.Diagnostics {
		if strings.Contains(d.Message(), "test.bad") &&
			strings.Contains(d.Message(), "expects string") &&
			strings.Contains(d.Message(), "number literal") {
			saw = true
			break
		}
	}
	assert.True(t, saw, "expected default-value type mismatch diagnostic for test.bad")
}

// TestAnnotationArgsAccepted verifies the happy path: well-typed
// arguments against an annotation with mixed param kinds produce no
// diagnostics. The `any`-typed param takes a resolvable enum-value
// reference — under the finalized Literal carrier, identifier
// arguments are enum-value references and must resolve.
func TestAnnotationArgsAccepted(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

enum Visibility {
  VISIBILITY_UNSPECIFIED = 0;
  PUBLIC = 1;
}

annotation ok(s: string, n: int32, b: bool, free: any);

@ok("hello", -42, true, Visibility.PUBLIC)
message M {}
`

	_, rep := compileForAnnotationTest(t, src)
	for _, d := range rep.Diagnostics {
		if isError(d) {
			t.Errorf("unexpected diagnostic: %s", d.Message())
		}
	}
}

// TestAnnotationArgUnresolvedEnumRef verifies that an identifier
// argument on an `any`-typed param that does not resolve to an enum
// value is diagnosed: the carrier lowers enum references resolved
// (RFC-001 §8.1), so there is no home for a bare name.
func TestAnnotationArgUnresolvedEnumRef(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

annotation ok(free: any);

@ok(anything.you.want)
message M {}
`

	_, rep := compileForAnnotationTest(t, src)
	var saw bool
	for _, d := range rep.Diagnostics {
		if isError(d) && strings.Contains(d.Message(), "anything.you.want") {
			saw = true
			break
		}
	}
	assert.True(t, saw, "expected unresolved enum-value reference diagnostic")
}

// isError reports whether d is an error or worse.
//
// [report.Level] runs most-severe-first — ICE(1), Error(2), Warning(3),
// Remark(4) — so error-or-worse is `<= report.Error`. The comparison is
// easy to write backwards, and every diagnostic loop in this package had
// it backwards: they skipped an ICE (a recovered compiler panic on the
// very source under test) while treating a warning as an error. Keeping
// the ordering in one predicate is what stops it drifting back;
// TestIsErrorSeverityOrdering pins it against every level.
func isError(d report.Diagnostic) bool { return d.Level() <= report.Error }

// TestIsErrorSeverityOrdering pins the direction of the severity
// comparison against every level [report] defines.
//
// Without it, nothing in this package fails when the comparison is
// written backwards: every fixture here produces Error-level
// diagnostics, and `<= Error` and `>= Error` agree on those. The two
// states that tell the comparisons apart — a diagnostic that is a
// warning, and one that is an ICE — are not reachable from any
// compilable fixture, so they are constructed directly.
func TestIsErrorSeverityOrdering(t *testing.T) {
	t.Parallel()

	var rep report.Report
	rep.Fatalf("synthetic ice")
	rep.Errorf("synthetic error")
	rep.Warnf("synthetic warning")
	rep.Remarkf("synthetic remark")
	require.Len(t, rep.Diagnostics, 4)

	assert.True(t, isError(rep.Diagnostics[0]), "an ICE is worse than an error")
	assert.True(t, isError(rep.Diagnostics[1]), "an error is an error")
	assert.False(t, isError(rep.Diagnostics[2]), "a warning is not an error")
	assert.False(t, isError(rep.Diagnostics[3]), "a remark is not an error")

	assert.True(t, hasErrorContaining(&rep, "synthetic", "ice"),
		"a recovered compiler panic must satisfy an error assertion")
	assert.True(t, hasErrorContaining(&rep, "synthetic error"))
	assert.False(t, hasErrorContaining(&rep, "synthetic warning"),
		"a warning must not satisfy an error assertion")
	assert.False(t, hasErrorContaining(&rep, "synthetic remark"))
}

// hasErrorContaining reports whether the report contains an error-
// level diagnostic whose message contains every needle.
func hasErrorContaining(rep *report.Report, needles ...string) bool {
	for _, d := range rep.Diagnostics {
		if !isError(d) {
			continue
		}
		all := true
		for _, n := range needles {
			if !strings.Contains(d.Message(), n) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

// TestAnnotationArgCallArityMismatch verifies RFC-001 §8.1 arity
// verification: a call site whose name resolves to a declared
// function must match the declaration's parameter count.
func TestAnnotationArgCallArityMismatch(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

function in_region(value: string, regions: string);

annotation validate(rule: expression);

@validate(in_region(this))
message M {}
`

	_, rep := compileForAnnotationTest(t, src)
	assert.True(t,
		hasErrorContaining(rep, "test.in_region", "1 argument(s)", "declares 2"),
		"expected arity mismatch diagnostic, got: %v", rep.Diagnostics)
}

// TestAnnotationArgBuiltinsUndiagnosed verifies that call sites whose
// names do not resolve to a declared function are presumed engine
// builtins: not diagnosed, whatever their arity.
func TestAnnotationArgBuiltinsUndiagnosed(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

annotation validate(rule: expression);

@validate(matches(this, "^[a-z]+$"))
@validate(now())
@validate(this.size() < 280)
message M {}
`

	_, rep := compileForAnnotationTest(t, src)
	for _, d := range rep.Diagnostics {
		if isError(d) {
			t.Errorf("unexpected diagnostic: %s", d.Message())
		}
	}
}

// TestAnnotationArgNamedBinding verifies named-argument binding and
// its diagnostics: unknown names, positional-after-named, and double
// binding.
func TestAnnotationArgNamedBinding(t *testing.T) {
	t.Parallel()

	t.Run("unknown", func(t *testing.T) {
		t.Parallel()
		_, rep := compileForAnnotationTest(t, `syntax = "proto3";
package test;
annotation validate(rule: expression, code: string);
@validate(this != 0, oops = "x")
message M {}
`)
		assert.True(t, hasErrorContaining(rep, "unknown named argument", "oops"),
			"expected unknown-named-argument diagnostic, got: %v", rep.Diagnostics)
	})

	t.Run("positional-after-named", func(t *testing.T) {
		t.Parallel()
		_, rep := compileForAnnotationTest(t, `syntax = "proto3";
package test;
annotation validate(rule: expression, code: string);
@validate(code = "x", this != 0)
message M {}
`)
		assert.True(t, hasErrorContaining(rep, "positional argument after named argument"),
			"expected ordering diagnostic, got: %v", rep.Diagnostics)
	})

	t.Run("double-binding", func(t *testing.T) {
		t.Parallel()
		_, rep := compileForAnnotationTest(t, `syntax = "proto3";
package test;
annotation validate(rule: expression, code: string);
@validate(this != 0, code = "x", code = "y")
message M {}
`)
		assert.True(t, hasErrorContaining(rep, "bound more than once", "code"),
			"expected double-binding diagnostic, got: %v", rep.Diagnostics)
	})

	t.Run("accepted", func(t *testing.T) {
		t.Parallel()
		_, rep := compileForAnnotationTest(t, `syntax = "proto3";
package test;
annotation validate(rule: expression, code: string = "", message: string = "");
@validate(this != 0, code = "tier.invalid", message = "bad tier")
message M {}
`)
		for _, d := range rep.Diagnostics {
			if isError(d) {
				t.Errorf("unexpected diagnostic: %s", d.Message())
			}
		}
	})
}

// TestAnnotationArgOpaqueOnNonExpression verifies that an engine-
// expression fragment bound to a non-expression parameter is
// diagnosed: only `expression`-typed params keep opaque captures.
func TestAnnotationArgOpaqueOnNonExpression(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

annotation tag(name: string);

@tag(this == 1)
message M {}
`

	_, rep := compileForAnnotationTest(t, src)
	assert.True(t, hasErrorContaining(rep, "must be a literal or a qualified name"),
		"expected opaque-capture diagnostic, got: %v", rep.Diagnostics)
}

// TestAnnotationArgListHomogeneity verifies RFC-001 §8.1 list rules:
// heterogeneous lists and mixed enum types are diagnosed; homogeneous
// lists (including nested) are accepted.
func TestAnnotationArgListHomogeneity(t *testing.T) {
	t.Parallel()

	t.Run("heterogeneous", func(t *testing.T) {
		t.Parallel()
		_, rep := compileForAnnotationTest(t, `syntax = "proto3";
package test;
annotation allowed(values: any);
@allowed(["US", 42])
message M {}
`)
		assert.True(t, hasErrorContaining(rep, "heterogeneous list literal"),
			"expected homogeneity diagnostic, got: %v", rep.Diagnostics)
	})

	t.Run("mixed-enums", func(t *testing.T) {
		t.Parallel()
		_, rep := compileForAnnotationTest(t, `syntax = "proto3";
package test;
enum A { A_UNSPECIFIED = 0; A_ONE = 1; }
enum B { B_UNSPECIFIED = 0; B_ONE = 1; }
annotation allowed(values: any);
@allowed([A_ONE, B_ONE])
message M {}
`)
		assert.True(t, hasErrorContaining(rep, "mixed enum types"),
			"expected mixed-enum diagnostic, got: %v", rep.Diagnostics)
	})

	t.Run("accepted", func(t *testing.T) {
		t.Parallel()
		_, rep := compileForAnnotationTest(t, `syntax = "proto3";
package test;
annotation allowed(values: any);
@allowed(["US", "CA", "GB"])
@allowed([1, 2, 3])
@allowed([[1], [2, 3]])
@allowed([])
message M {}
`)
		for _, d := range rep.Diagnostics {
			if isError(d) {
				t.Errorf("unexpected diagnostic: %s", d.Message())
			}
		}
	})
}

// TestAnnotationArgMessageLiteral verifies the message-literal rules:
// the explicit-typing requirement under `any` params, unknown-field
// diagnosis, and the temporary not-yet-lowered cap.
func TestAnnotationArgMessageLiteral(t *testing.T) {
	t.Parallel()

	t.Run("explicit-type-required", func(t *testing.T) {
		t.Parallel()
		_, rep := compileForAnnotationTest(t, `syntax = "proto3";
package test;
annotation sample(value: any);
@sample({currency: "USD"})
message M {}
`)
		assert.True(t, hasErrorContaining(rep, "requires an explicit type name"),
			"expected explicit-typing diagnostic, got: %v", rep.Diagnostics)
	})

	t.Run("unknown-field", func(t *testing.T) {
		t.Parallel()
		_, rep := compileForAnnotationTest(t, `syntax = "proto3";
package test;
message Money {
  string currency = 1;
}
annotation sample(value: any);
@sample(Money{amount: 5})
message M {}
`)
		assert.True(t, hasErrorContaining(rep, "cannot resolve", "test.Money"),
			"expected unknown-field diagnostic, got: %v", rep.Diagnostics)
	})

	t.Run("accepted", func(t *testing.T) {
		t.Parallel()
		_, rep := compileForAnnotationTest(t, `syntax = "proto3";
package test;
message Inner {
  bool ok = 1;
}
message Money {
  string currency = 1;
  int64 units = 2;
  repeated string tags = 3;
  Inner inner = 4;
}
annotation sample(value: any);
@sample(Money{currency: "USD", units: 5, tags: ["a", "b"], inner: {ok: true}})
message M {}
`)
		for _, d := range rep.Diagnostics {
			if isError(d) {
				t.Errorf("unexpected diagnostic: %s", d.Message())
			}
		}
	})

	t.Run("field-type-mismatch", func(t *testing.T) {
		t.Parallel()
		_, rep := compileForAnnotationTest(t, `syntax = "proto3";
package test;
message Money {
  int64 units = 1;
}
annotation sample(value: any);
@sample(Money{units: "not a number"})
message M {}
`)
		var saw bool
		for _, d := range rep.Diagnostics {
			if isError(d) {
				saw = true
				break
			}
		}
		assert.True(t, saw, "expected a field-value type mismatch diagnostic")
	})

	t.Run("map-field-rejected", func(t *testing.T) {
		t.Parallel()
		_, rep := compileForAnnotationTest(t, `syntax = "proto3";
package test;
message Money {
  map<string, string> attrs = 1;
}
annotation sample(value: any);
@sample(Money{attrs: [{key: "k", value: "v"}]})
message M {}
`)
		assert.True(t, hasErrorContaining(rep, "map-typed field", "attrs"),
			"expected map-field rejection, got: %v", rep.Diagnostics)
	})

	t.Run("wrong-explicit-type-on-message-param", func(t *testing.T) {
		t.Parallel()
		_, rep := compileForAnnotationTest(t, `syntax = "proto3";
package test;
message Money {
  string currency = 1;
}
message Other {
  string x = 1;
}
annotation sample(value: Money);
@sample(Other{x: "y"})
message M {}
`)
		assert.True(t, hasErrorContaining(rep, "typed `test.Other`", "declares `test.Money`"),
			"expected type-mismatch diagnostic, got: %v", rep.Diagnostics)
	})
}

// TestAnnotationArgEnumQualifiedForm verifies both accepted enum-
// reference spellings resolve: the value's scoped name and the
// enum-qualified form.
func TestAnnotationArgEnumQualifiedForm(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

enum Visibility {
  VISIBILITY_UNSPECIFIED = 0;
  PUBLIC = 1;
}

annotation scope(v: Visibility);

@scope(PUBLIC)
message M1 {}

@scope(Visibility.PUBLIC)
message M2 {}

@scope(test.Visibility.PUBLIC)
message M3 {}
`

	_, rep := compileForAnnotationTest(t, src)
	for _, d := range rep.Diagnostics {
		if isError(d) {
			t.Errorf("unexpected diagnostic: %s", d.Message())
		}
	}
}

// compileTwoForAnnotationTest compiles main.proto against lib.proto.
func compileTwoForAnnotationTest(t *testing.T, mainSrc, libSrc string) (*ir.File, *report.Report) {
	t.Helper()
	opener := source.NewMap(map[string]*source.File{
		"main.proto": source.NewFile("main.proto", mainSrc),
		"lib.proto":  source.NewFile("lib.proto", libSrc),
	})
	allOpeners := &source.Openers{opener, source.WKTs()}

	exec := incremental.New()
	sess := new(ir.Session)
	results, rep, err := incremental.Run(t.Context(), exec, queries.IR{
		Opener:  allOpeners,
		Session: sess,
		Path:    "main.proto",
	})
	require.NoError(t, err)
	require.NotNil(t, rep)
	require.Len(t, results, 1)
	require.NotNil(t, results[0].Value)
	return results[0].Value, rep
}

// TestAnnotationUseBareImportResolution verifies the RFC-001
// §5.2/§5.3 spelling: a bare `@validate` resolves to an imported
// `annotation` declaration whose package is not in the using file's
// scope chain, and a qualified spelling keeps working.
func TestAnnotationUseBareImportResolution(t *testing.T) {
	t.Parallel()

	const lib = `syntax = "proto3";
package protowire.schema.v1;

annotation validate(rule: expression, code: string = "");
annotation required;
`
	const main = `syntax = "proto3";
package myco.users;

import "lib.proto";

message User {
  string email = 1
    @required
    @validate(this.size() > 0, code = "email.empty")
    @protowire.schema.v1.required;
}
`

	file, rep := compileTwoForAnnotationTest(t, main, lib)
	for _, d := range rep.Diagnostics {
		if isError(d) {
			t.Errorf("unexpected diagnostic: %s", d.Message())
		}
	}

	for ty := range seq.Values(file.AllTypes()) {
		if ty.Name() != "User" {
			continue
		}
		for f := range seq.Values(ty.Members()) {
			var targets []string
			for u := range seq.Values(f.Annotations()) {
				targets = append(targets, string(u.Target().FullName()))
			}
			assert.Equal(t, []string{
				"protowire.schema.v1.required",
				"protowire.schema.v1.validate",
				"protowire.schema.v1.required",
			}, targets)
		}
	}
}

// TestAnnotationUseBareImportAmbiguity verifies that a bare name
// matching `annotation` declarations in more than one visible import
// is diagnosed as ambiguous.
func TestAnnotationUseBareImportAmbiguity(t *testing.T) {
	t.Parallel()

	opener := source.NewMap(map[string]*source.File{
		"a.proto": source.NewFile("a.proto", `syntax = "proto3";
package liba;
annotation tag(name: string);
`),
		"b.proto": source.NewFile("b.proto", `syntax = "proto3";
package libb;
annotation tag(name: string);
`),
		"main.proto": source.NewFile("main.proto", `syntax = "proto3";
package myco;
import "a.proto";
import "b.proto";
@tag("x")
message M {}
`),
	})
	allOpeners := &source.Openers{opener, source.WKTs()}

	exec := incremental.New()
	sess := new(ir.Session)
	_, rep, err := incremental.Run(t.Context(), exec, queries.IR{
		Opener:  allOpeners,
		Session: sess,
		Path:    "main.proto",
	})
	require.NoError(t, err)

	assert.True(t, hasErrorContaining(rep, "ambiguous annotation", "tag"),
		"expected ambiguity diagnostic, got: %v", rep.Diagnostics)
}

// TestAnnotationParamDefaultEnumRef verifies enum-value references
// in parameter defaults: both spellings resolve relative to the
// declaration's scope, wrong-enum defaults are diagnosed, and
// unresolvable identifiers on `any`-typed params are errors.
func TestAnnotationParamDefaultEnumRef(t *testing.T) {
	t.Parallel()

	t.Run("accepted", func(t *testing.T) {
		t.Parallel()
		_, rep := compileForAnnotationTest(t, `syntax = "proto3";
package test;
enum Tier {
  TIER_UNSPECIFIED = 0;
  TIER_FREE = 1;
}
annotation plan(t: Tier = TIER_FREE, u: Tier = Tier.TIER_FREE, free: any = TIER_FREE);
`)
		for _, d := range rep.Diagnostics {
			if isError(d) {
				t.Errorf("unexpected diagnostic: %s", d.Message())
			}
		}
	})

	t.Run("wrong-enum", func(t *testing.T) {
		t.Parallel()
		_, rep := compileForAnnotationTest(t, `syntax = "proto3";
package test;
enum A { A_UNSPECIFIED = 0; }
enum B { B_UNSPECIFIED = 0; }
annotation plan(a: A = B_UNSPECIFIED);
`)
		assert.True(t, hasErrorContaining(rep, "expects a value of enum `test.A`", "test.B"),
			"expected wrong-enum diagnostic, got: %v", rep.Diagnostics)
	})

	t.Run("unresolved-on-any", func(t *testing.T) {
		t.Parallel()
		_, rep := compileForAnnotationTest(t, `syntax = "proto3";
package test;
annotation plan(free: any = nothing.here);
`)
		assert.True(t, hasErrorContaining(rep, "nothing.here"),
			"expected unresolved-reference diagnostic, got: %v", rep.Diagnostics)
	})
}

// TestAnnotationPlacementLegalized verifies RFC-001 §5.1 hybrid
// placement is enforced by production: leading on fields/enum
// values/type/function declarations and trailing on rpc methods are
// diagnosed.
func TestAnnotationPlacementLegalized(t *testing.T) {
	t.Parallel()

	t.Run("leading-on-field", func(t *testing.T) {
		t.Parallel()
		_, rep := compileForAnnotationTest(t, `syntax = "proto3";
package test;
annotation required;
message M {
  @required
  string s = 1;
}
`)
		assert.True(t, hasErrorContaining(rep, "leading annotation on a field"),
			"expected placement diagnostic, got: %v", rep.Diagnostics)
	})

	t.Run("leading-on-enum-value", func(t *testing.T) {
		t.Parallel()
		_, rep := compileForAnnotationTest(t, `syntax = "proto3";
package test;
annotation described;
enum E {
  @described
  E_UNSET = 0;
}
`)
		assert.True(t, hasErrorContaining(rep, "leading annotation on an enum value"),
			"expected placement diagnostic, got: %v", rep.Diagnostics)
	})

	t.Run("trailing-on-method", func(t *testing.T) {
		t.Parallel()
		_, rep := compileForAnnotationTest(t, `syntax = "proto3";
package test;
annotation described;
message M {}
service S {
  rpc Ping(M) returns (M) @described;
}
`)
		assert.True(t, hasErrorContaining(rep, "trailing annotation on a method"),
			"expected placement diagnostic, got: %v", rep.Diagnostics)
	})

	t.Run("leading-on-type", func(t *testing.T) {
		t.Parallel()
		_, rep := compileForAnnotationTest(t, `syntax = "proto3";
package test;
annotation described;
@described
type Email = string;
`)
		assert.True(t, hasErrorContaining(rep, "leading annotation on a type"),
			"expected placement diagnostic, got: %v", rep.Diagnostics)
	})

	t.Run("leading-on-function", func(t *testing.T) {
		t.Parallel()
		_, rep := compileForAnnotationTest(t, `syntax = "proto3";
package test;
annotation described;
@described
function f(x: string);
`)
		assert.True(t, hasErrorContaining(rep, "leading annotation on a function"),
			"expected placement diagnostic, got: %v", rep.Diagnostics)
	})

	t.Run("correct-placements-accepted", func(t *testing.T) {
		t.Parallel()
		_, rep := compileForAnnotationTest(t, `syntax = "proto3";
package test;
annotation described(text: string = "");
message M {}
type Email = string @described;
function f(x: string) @described;
@described
message N {
  string s = 1 @described;
}
@described
enum E {
  E_UNSET = 0 @described;
}
@described
service S {
  @described
  rpc Ping(M) returns (M);
}
`)
		for _, d := range rep.Diagnostics {
			if isError(d) {
				t.Errorf("unexpected diagnostic: %s", d.Message())
			}
		}
	})
}

// TestAnnotationSensitiveReservedClass verifies RFC-001 §6.7 rule 1:
// a `class` value on the canonical `@sensitive` annotation that sits
// in the reserved `protowire.` namespace is rejected — for the named,
// positional, and exact-namespace spellings alike — while org-defined
// class names and lookalike prefixes compile clean.
func TestAnnotationSensitiveReservedClass(t *testing.T) {
	t.Parallel()

	const lib = `syntax = "proto3";
package protowire.schema.v1;

annotation sensitive(class: string = "");
`
	const main = `syntax = "proto3";
package fixtures.badclass;

import "lib.proto";

message Config {
  string token = 1 @sensitive(class = "protowire.secret");
  string named = 2 @sensitive("protowire.pii");
  string root = 3 @sensitive(class = "protowire");
  string fine = 4 @sensitive(class = "credentials");
  string bare = 5 @sensitive;
  string similar = 6 @sensitive(class = "protowirex.pii");
  string empty = 7 @sensitive(class = "");
}
`

	_, rep := compileTwoForAnnotationTest(t, main, lib)

	for _, reserved := range []string{"protowire.secret", "protowire.pii", "protowire"} {
		assert.True(t,
			hasErrorContaining(rep, "sensitivity class", reserved, "reserved"),
			"expected reserved-class diagnostic for %q, got: %v", reserved, rep.Diagnostics)
	}

	var count int
	for _, d := range rep.Diagnostics {
		if isError(d) {
			count++
			assert.Contains(t, d.Message(), "is reserved", "unexpected diagnostic: %s", d.Message())
		}
	}
	assert.Equal(t, 3, count, "org-defined, bare, lookalike, and empty uses must compile clean")
}

// TestAnnotationSensitiveReservedClassNonCanonical verifies the
// reserved-class check keys on the resolved FQN
// `protowire.schema.v1.sensitive`: a user annotation that happens to
// be named `sensitive` accepts any class value.
func TestAnnotationSensitiveReservedClassNonCanonical(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package myco;

annotation sensitive(class: string = "");

message Config {
  string token = 1 @sensitive(class = "protowire.secret");
}
`

	_, rep := compileForAnnotationTest(t, src)
	for _, d := range rep.Diagnostics {
		if isError(d) {
			t.Errorf("unexpected diagnostic: %s", d.Message())
		}
	}
}

// TestAnnotationArgMessageListElements verifies RFC-001 §8.1 message
// literals as list elements (the LiteralValue.literal kind): typed
// elements compose inside lists — bare or qualified type names,
// multi-element and nested-list forms — while untyped elements still
// trip the explicit-typing rule and unknown type names get the
// standard resolution diagnostics.
func TestAnnotationArgMessageListElements(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

annotation meta(value: any);

message Item { int32 status = 1; }

@meta([Item{status: 1}])
message Single {}

@meta([Item{status: 1}, Item{status: 2}])
message Multi {}

@meta([test.Item{status: 1}])
message Qualified {}

@meta([[Item{status: 1}], [Item{status: 2}]])
message Nested {}
`

	file, rep := compileForAnnotationTest(t, src)
	for _, d := range rep.Diagnostics {
		if isError(d) {
			t.Errorf("unexpected diagnostic: %s", d.Message())
		}
	}

	// The element literal is evaluated against its explicit type and
	// recorded for descriptor production.
	for ty := range seq.Values(file.AllTypes()) {
		if ty.Name() != "Single" {
			continue
		}
		uses := ty.Annotations()
		require.Equal(t, 1, uses.Len())
		u := uses.At(0)
		bindings := u.ArgBindings()
		require.Len(t, bindings, 1)
		value := bindings[0].Arg.Value()
		require.Equal(t, ast.ExprKindArray, value.Kind())
		elems := value.AsArray().Elements()
		require.Equal(t, 1, elems.Len())
		msg := u.MessageLiteralElem(elems.At(0))
		require.False(t, msg.IsZero(), "element literal should be evaluated and recorded")
		assert.Equal(t, "test.Item", string(msg.Type().FullName()))
	}
}

// TestAnnotationArgMessageListElementUntyped verifies the
// explicit-typing rule for message-literal list elements: under an
// `any`-typed context the element's type cannot come from the
// declaration, so an untyped `{...}` element is diagnosed.
func TestAnnotationArgMessageListElementUntyped(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

annotation meta(value: any);

message Item { int32 status = 1; }

@meta([{status: 1}])
message Target {}
`

	_, rep := compileForAnnotationTest(t, src)
	assert.True(t,
		hasErrorContaining(rep, "message-literal list element", "explicit type name"),
		"expected explicit-type-name diagnostic, got: %v", rep.Diagnostics)
}

// TestAnnotationArgMessageListElementBadType verifies that a message
// list element whose type name does not resolve gets the standard
// symbol-resolution diagnostics.
func TestAnnotationArgMessageListElementBadType(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

annotation meta(value: any);

@meta([Unknown{status: 1}])
message Target {}
`

	_, rep := compileForAnnotationTest(t, src)
	assert.True(t, hasErrorContaining(rep, "Unknown"),
		"expected resolution diagnostic, got: %v", rep.Diagnostics)
}

// TestAnnotationArgMessageListElementHeterogeneous verifies that
// message elements participate in the §8.1 homogeneity check.
func TestAnnotationArgMessageListElementHeterogeneous(t *testing.T) {
	t.Parallel()

	const src = `syntax = "proto3";
package test;

annotation meta(value: any);

message Item { int32 status = 1; }

@meta([Item{status: 1}, "x"])
message Target {}
`

	_, rep := compileForAnnotationTest(t, src)
	assert.True(t, hasErrorContaining(rep, "heterogeneous list literal"),
		"expected homogeneity diagnostic, got: %v", rep.Diagnostics)
}

// TestAnnotationUseEnumArgRejectsLiterals pins RFC-001 §5.1 rule 4: an
// enum-typed parameter takes a `qualifiedIdent` — a bare or qualified
// value name — which the linker resolves into an EnumLiteral. A scalar
// literal is not a spelling for an enum value, so every one of them is a
// compile error rather than some other lowering.
//
// This is the behaviour issue #153 reported as missing. It was not: the
// diagnostic was already raised here, and the measurement behind that
// issue was taken through a test harness that discarded the report. The
// pin lives in ir because ir is where the diagnostic is raised.
func TestAnnotationUseEnumArgRejectsLiterals(t *testing.T) {
	t.Parallel()

	const head = `syntax = "proto3";
package test;
enum Color { RED = 0; GREEN = 1; }
annotation e(value: Color);
`

	t.Run("accepted", func(t *testing.T) {
		t.Parallel()
		for _, use := range []string{"@e(GREEN)", "@e(Color.GREEN)", "@e(RED)"} {
			_, rep := compileForAnnotationTest(t, head+use+"\nmessage M {}\n")
			for _, d := range rep.Diagnostics {
				if isError(d) {
					t.Errorf("%s: unexpected diagnostic: %s", use, d.Message())
				}
			}
		}
	})

	// Each of these lowered silently to a scalar before anyone looked at
	// the report: a float truncated to int_value, an out-of-range integer
	// carried verbatim, a string kept as string_value.
	for _, tc := range []struct{ name, lit string }{
		{name: "float", lit: "1.5"},
		{name: "in_range_int", lit: "1"},
		{name: "out_of_range_int", lit: "999"},
		{name: "negative_int", lit: "-3"},
		{name: "string", lit: `"GREEN"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, rep := compileForAnnotationTest(t, head+"@e("+tc.lit+")\nmessage M {}\n")
			assert.True(t,
				hasErrorContaining(rep, "expects a value of enum `test.Color`"),
				"a %s literal on an enum parameter must be diagnosed, got: %v", tc.name, rep.Diagnostics)
		})
	}
}

// TestAnnotationScalarArgRange pins the range check on a declared integer
// parameter: a literal that cannot be represented by the type the author
// asked for is a compile error rather than a silently wrapped value.
//
// Before this, none of the rejected cases below were diagnosed. `1e100`
// saturated to MaxUint64 and reinterpreted to -1, a negative literal was
// accepted by an unsigned parameter, and a value past the declared width
// simply wrapped — and a consumer reading the carrier could not recover
// any of it (#165).
//
// Every bound is tested from both sides, one apart, because an off-by-one
// here is invisible: the accepted side would still compile and the
// rejected side would still be rejected.
func TestAnnotationScalarArgRange(t *testing.T) {
	t.Parallel()

	compile := func(t *testing.T, param, lit string) *report.Report {
		t.Helper()
		_, rep := compileForAnnotationTest(t, `syntax = "proto3";
package test;
message Foo { int32 x = 1; }
annotation a(value: `+param+`);
@a(`+lit+`)
message M {}
`)
		return rep
	}

	t.Run("accepted", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct{ param, lit string }{
			{"int32", "2147483647"},            // MaxInt32
			{"int32", "-2147483648"},           // MinInt32
			{"int64", "9223372036854775807"},   // MaxInt64
			{"int64", "-9223372036854775808"},  // MinInt64
			{"uint32", "4294967295"},           // MaxUint32
			{"uint64", "18446744073709551615"}, // MaxUint64
			{"uint32", "0"},
			{"int32", "3"},
			// The `sint`/`fixed`/`sfixed` spellings share the bounds of the
			// width they encode; each is at its maximum here, one below the
			// rejected row of the same name.
			{"sfixed32", "2147483647"},
			{"sint32", "-2147483648"},
			{"fixed32", "4294967295"},
			{"sint64", "9223372036854775807"},
			{"fixed64", "18446744073709551615"},
			// Non-decimal spellings reach the same check: the bound is on
			// the value, not on how it is written.
			{"int32", "0x7FFFFFFF"},
			// A signed type takes `-0`: it reaches the range check with a
			// magnitude of zero, which fits. On an UNSIGNED type it is an
			// error — see negative_zero_on_uint32 below, and
			// TestNegativeZeroAgreesBetweenFieldAndParameter for why.
			{"int32", "-0"},
			{"int64", "-0"},
			{"int32", "-0.4"},
			// A fraction that fits is not a range error; it still lowers.
			// Different question, deliberately untouched.
			{"int32", "1.5"},
			// The ROUNDED value is what has to fit (#167 — Int rounds to
			// nearest, half away from zero), so the bound sits half a step
			// lower than truncation would put it: .4 rounds back inside,
			// while .5 rounds out and is rejected below.
			{"int32", "2147483647.4"},
			{"int32", "-2147483648.4"},
			// Float scalars have no integer bound to exceed.
			{"double", "1e100"},
			{"float", "1.5"},
		} {
			rep := compile(t, tc.param, tc.lit)
			for _, d := range rep.Diagnostics {
				if isError(d) {
					t.Errorf("%s(%s): unexpected diagnostic: %s", tc.param, tc.lit, d.Message())
				}
			}
		}
	})

	for _, tc := range []struct{ name, param, lit, want string }{
		{"int32_above_max", "int32", "2147483648", "out of range for `int32`"},
		{"int32_below_min", "int32", "-2147483649", "out of range for `int32`"},
		{"int64_above_max", "int64", "9223372036854775808", "out of range for `int64`"},
		{"int64_below_min", "int64", "-9223372036854775809", "out of range for `int64`"},
		{"uint32_above_max", "uint32", "4294967296", "out of range for `uint32`"},
		// One past MaxUint64. float64 rounds this and MaxUint64 to the same
		// number, so a magnitude comparison misses it; the check tests
		// whether the value is whole instead.
		{"uint64_above_max", "uint64", "18446744073709551616", "out of range for `uint64`"},
		{"int32_saturating_exponent", "int32", "1e100", "out of range for `int32`"},
		{"uint64_saturating_exponent", "uint64", "1e100", "out of range for `uint64`"},
		{"negative_on_uint32", "uint32", "-3", "is negative, but `uint32` is unsigned"},
		{"negative_on_uint64", "uint64", "-1", "is negative, but `uint64` is unsigned"},
		// The `sint`/`fixed`/`sfixed` spellings map onto the same bounds;
		// each row is one past the accepted row of the same name above, so a
		// mapping that pointed at the wrong width would fail here.
		{"sfixed32_above_max", "sfixed32", "2147483648", "out of range for `sfixed32`"},
		{"sint32_below_min", "sint32", "-2147483649", "out of range for `sint32`"},
		{"fixed32_above_max", "fixed32", "4294967296", "out of range for `fixed32`"},
		{"sint64_above_max", "sint64", "9223372036854775808", "out of range for `sint64`"},
		{"negative_on_fixed64", "fixed64", "-1", "is negative, but `fixed64` is unsigned"},
		{"hex_above_max", "int32", "0x100000000", "out of range for `int32`"},

		// A fraction lowers truncated, so the TRUNCATION is what has to
		// fit. Skipping the check for every inexact literal let a value
		// straight past the bound through behind a `.5` — `99999999999.5`
		// on an `int32` reached the carrier as `int_value: 99999999999`,
		// and `-1.5` on an unsigned parameter as `int_value: -1`, which is
		// exactly what the two checks above exist to prevent.
		{"fraction_above_max", "int32", "99999999999.5", "out of range for `int32`"},
		{"fraction_one_past_max", "int32", "2147483648.5", "out of range for `int32`"},
		// Rounding, not truncation, decides: this rounds UP to 2147483648,
		// one past the ceiling, where truncation would have kept it inside.
		{"fraction_rounds_past_max", "int32", "2147483647.5", "out of range for `int32`"},
		{"fraction_rounds_past_min", "int32", "-2147483648.5", "out of range for `int32`"},
		// Rounds away from zero to a magnitude of 1, so it is a negative
		// value on an unsigned parameter, not the `-0` case.
		{"negative_half_on_uint32", "uint32", "-0.5", "is negative, but `uint32` is unsigned"},
		// Any negated literal on an unsigned type, whatever its magnitude
		// (#169). `-0` is the case that used to be accepted here while the
		// equivalent field was rejected.
		{"negative_zero_on_uint32", "uint32", "-0", "is negative, but `uint32` is unsigned"},
		{"negative_zero_on_uint64", "uint64", "-0", "is negative, but `uint64` is unsigned"},
		{"negative_zero_fraction_on_uint32", "uint32", "-0.4", "is negative, but `uint32` is unsigned"},
		{"fraction_one_past_min", "int32", "-2147483649.5", "out of range for `int32`"},
		{"negative_fraction_on_uint32", "uint32", "-1.5", "is negative, but `uint32` is unsigned"},

		// A list literal is not a scalar, and `repeated` is not a spellable
		// parameter type — but validateScalarArg had no case for the shape,
		// so `@a([1e100, 5])` on an `int32` parameter compiled and lowered
		// to a list, walking around the range check with two brackets.
		{"list_on_int32", "int32", "[1e100, 5]", "expects int32, got a list literal"},
		{"list_on_uint32", "uint32", "[-3]", "expects uint32, got a list literal"},
		{"message_literal_on_int32", "int32", "Foo{x: 1}", "expects int32, got a message literal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rep := compile(t, tc.param, tc.lit)
			assert.True(t, hasErrorContaining(rep, tc.want),
				"%s(%s) must be diagnosed, got: %v", tc.param, tc.lit, rep.Diagnostics)
		})
	}
}

// TestNegativeZeroAgreesBetweenFieldAndParameter is the point of #169: the
// package has two integer-range checkers, and they used to disagree.
//
// `checkIntBounds` (ir/lower_eval.go) errors on any negated literal for an
// unsigned type, so `Foo{x: -0}` on a `uint32` field was rejected, while
// `checkIntegerRange` accepted `@a(-0)` on a `uint32` parameter. Either
// answer is defensible; one package holding both is not, and a reader has
// no way to predict which they will meet.
//
// The test asserts they agree rather than asserting a particular answer, so
// it keeps its teeth if the shared answer is ever revisited.
func TestNegativeZeroAgreesBetweenFieldAndParameter(t *testing.T) {
	t.Parallel()

	// `-0` bound to a uint32 field, through a message literal.
	_, fieldRep := compileForAnnotationTest(t, `syntax = "proto3";
package test;
message Foo { uint32 x = 1; }
annotation m(value: Foo);
@m(Foo{x: -0})
message M {}
`)
	// `-0` bound to a uint32 parameter.
	_, paramRep := compileForAnnotationTest(t, `syntax = "proto3";
package test;
annotation a(value: uint32);
@a(-0)
message M {}
`)

	fieldErrs := countErrors(fieldRep)
	paramErrs := countErrors(paramRep)
	assert.Equal(t, fieldErrs > 0, paramErrs > 0,
		"a uint32 field and a uint32 parameter must answer `-0` the same way; "+
			"field diagnostics: %v, parameter diagnostics: %v",
		fieldRep.Diagnostics, paramRep.Diagnostics)

	// And the answer they agree on today.
	assert.Positive(t, fieldErrs, "the field rejects `-0`")
	assert.Positive(t, paramErrs, "so the parameter must too")
}

func countErrors(rep *report.Report) int {
	var n int
	for _, d := range rep.Diagnostics {
		if isError(d) {
			n++
		}
	}
	return n
}

// compileCarrier compiles `@deflt(lit)` on a field of the given type and
// reports whether it was diagnosed, without requiring a clean compile.
func compileCarrier(t *testing.T, fieldType, lit string) (*report.Report, bool) {
	t.Helper()
	imp := ""
	if strings.HasPrefix(fieldType, "google.protobuf.") {
		imp = "import \"google/protobuf/wrappers.proto\";\n"
	}
	_, rep := compileForAnnotationTest(t, `syntax = "proto3";
package test;
`+imp+`annotation deflt(value: any);
message M { `+fieldType+` f = 1 @deflt(`+lit+`); }
`)
	for _, d := range rep.Diagnostics {
		if isError(d) {
			return rep, true
		}
	}
	return rep, false
}

// TestCarrierBoundRejectsWhatTheAnnotatedTypeCannotHold is #177: a literal
// in (MaxInt64, MaxUint64] lowers into `int_value`, an int64, so on a
// SIGNED 64-bit carrier it wraps to a negative — and lands inside the
// type's own range, which is why nothing downstream reported it either.
// The 32-bit carriers were caught only because the wrapped value still did
// not fit them.
func TestCarrierBoundRejectsWhatTheAnnotatedTypeCannotHold(t *testing.T) {
	t.Parallel()

	t.Run("rejected", func(t *testing.T) {
		t.Parallel()
		for _, ft := range []string{
			"int64", "sint64", "sfixed64", // #177's headline
			"int32", "sint32", "sfixed32", "uint32", "fixed32",
			"google.protobuf.Int64Value", "google.protobuf.Int32Value",
		} {
			_, diagnosed := compileCarrier(t, ft, "1e19")
			assert.True(t, diagnosed, "%s carrier must reject a literal it cannot hold", ft)
		}
	})

	t.Run("accepted", func(t *testing.T) {
		t.Parallel()
		for _, tc := range [][2]string{
			// An unsigned carrier holds it and recovers it from its own type.
			{"uint64", "1e19"}, {"fixed64", "1e19"},
			{"google.protobuf.UInt64Value", "1e19"},
			// The bound is one apart from both ends.
			{"int64", "9223372036854775807"},
			{"uint64", "18446744073709551615"},
			{"int32", "2147483647"},
			// A float carrier is routed to double_value and never bounded.
			{"double", "1e19"}, {"float", "1e19"},
			{"google.protobuf.DoubleValue", "1e19"},
		} {
			rep, diagnosed := compileCarrier(t, tc[0], tc[1])
			assert.False(t, diagnosed, "%s must accept %s, got: %v", tc[0], tc[1], rep.Diagnostics)
		}
	})
}

// TestCarrierBoundOnlyRejectsWhatLowersAsInt pins the guard the bound needs
// to mirror: a literal routed to `double_value` must not be bounded by
// int64's range.
//
// The bound restates buildLiteralArg's routing, so the two can drift. These
// are the values that lower as a double for reasons OTHER than the
// carrier's type — float spelling, past uint64, and a negative magnitude
// past int64 — and every one of them must compile on a carrier whose own
// range would reject it.
func TestCarrierBoundOnlyRejectsWhatLowersAsInt(t *testing.T) {
	t.Parallel()

	for _, lit := range []string{
		"1.5", // float-spelled and inexact
		// Float-spelled AND exact, and far outside the carrier's range.
		// This is the pair the guard exists for: every other value here is
		// caught by the exactness check instead, so without these two,
		// deleting the IsFloat guard passes the whole suite.
		"1.0e19",
		"10000000000000000000.0",
		"1e100",                   // past uint64, lowers as double (#165)
		"99999999999999999999999", // same
		"-18446744073709551615",   // negative magnitude past int64 (#166)
	} {
		rep, diagnosed := compileCarrier(t, "int32", lit)
		assert.False(t, diagnosed,
			"%s lowers as double_value and must not be bounded by int32: %v", lit, rep.Diagnostics)
	}
}

// TestCarrierBoundRejectsOnArbitraryPrecisionCarriers is #176. A carrier
// with no scalar of its own — protowire's pxf.BigInt, pxf.Decimal and
// pxf.BigFloat, matched here by shape rather than by import — still lowers
// its literal into `int_value`, so a value in the band reached a consumer
// with the WRONG SIGN, not merely reduced precision.
//
// Bounding by int64 makes that a compile error rather than a negative
// number. It does not give those types a faithful route; that needs a
// carrier member which can hold them, and is governance-gated.
func TestCarrierBoundRejectsOnArbitraryPrecisionCarriers(t *testing.T) {
	t.Parallel()

	compile := func(t *testing.T, msg, lit string) (*report.Report, bool) {
		t.Helper()
		_, rep := compileForAnnotationTest(t, `syntax = "proto3";
package pxf;
annotation deflt(value: any);
message `+msg+` { bytes abs = 1; bool negative = 2; }
message M { `+msg+` f = 1 @deflt(`+lit+`); }
`)
		for _, d := range rep.Diagnostics {
			if isError(d) {
				return rep, true
			}
		}
		return rep, false
	}

	for _, msg := range []string{"BigInt", "Decimal", "BigFloat"} {
		_, diagnosed := compile(t, msg, "1e19")
		assert.True(t, diagnosed, "pxf.%s must reject a band literal rather than sign-flip it", msg)

		// Below the band these carriers were always exact, and stay so.
		rep, diagnosed := compile(t, msg, "42")
		assert.False(t, diagnosed, "pxf.%s must still accept 42: %v", msg, rep.Diagnostics)
	}
}

// TestCarrierBoundDescendsIntoListArguments pins the bound against the
// shape it first missed: a LIST argument.
//
// fdp's buildListLiteral hands every element to the same buildArgValue the
// scalar form uses, with the same carrier, so `@deflt([1e19])` on an
// `int64` field wrapped to -8446744073709551616 exactly as `@deflt(1e19)`
// did — #177 verbatim, one shape over. A bound that only inspects the
// argument expression itself leaves that open, and nesting has to recurse
// because buildListElement lowers a nested list through buildArgValue too.
func TestCarrierBoundDescendsIntoListArguments(t *testing.T) {
	t.Parallel()

	t.Run("rejected", func(t *testing.T) {
		t.Parallel()
		for _, tc := range [][2]string{
			{"int64", "[1e19]"},     // #177's headline, in a list
			{"sint64", "[1e19]"},    //
			{"sfixed64", "[1e19]"},  //
			{"int32", "[1e19]"},     //
			{"int64", "[[1e19]]"},   // nested lists lower the same way
			{"int64", "[42, 1e19]"}, // the offending element is not first
			{"uint64", "[-1]"},      // negative-on-unsigned, in a list
			{"google.protobuf.Int64Value", "[1e19]"},
		} {
			_, diagnosed := compileCarrier(t, tc[0], tc[1])
			assert.True(t, diagnosed, "%s carrier must reject %s", tc[0], tc[1])
		}
	})

	t.Run("accepted", func(t *testing.T) {
		t.Parallel()
		for _, tc := range [][2]string{
			// Everything the scalar form accepts, a list of it accepts too.
			{"uint64", "[1e19]"}, {"fixed64", "[1e19]"},
			{"double", "[1e19]"}, {"float", "[1e19]"},
			{"int64", "[42, 9223372036854775807]"},
			{"int32", "[1.5, 1e100, -18446744073709551615]"},
			{"int32", "[]"},
			{"string", `["x", "y"]`},
		} {
			rep, diagnosed := compileCarrier(t, tc[0], tc[1])
			assert.False(t, diagnosed, "%s must accept %s, got: %v", tc[0], tc[1], rep.Diagnostics)
		}
	})
}

// TestCarrierBoundRejectsNegativeOnUnsignedCarrier pins the other arm of
// the carrier bound, which nothing else reaches: every unsigned case in
// the range tests is `1e19`, which takes the too-large path instead.
//
// `-0` is rejected along with `-1`, deliberately: a `-` prefix on an
// unsigned target is the error whatever the magnitude, which is what
// checkIntBounds (ir/lower_eval.go) already does for an unsigned FIELD and
// what #169/#171 settled for a parameter. A signed carrier still takes
// both.
func TestCarrierBoundRejectsNegativeOnUnsignedCarrier(t *testing.T) {
	t.Parallel()

	t.Run("rejected", func(t *testing.T) {
		t.Parallel()
		for _, tc := range [][2]string{
			{"uint64", "-1"}, {"fixed64", "-1"},
			{"uint32", "-1"}, {"fixed32", "-1"},
			{"uint64", "-0"}, {"uint32", "-0"},
			{"google.protobuf.UInt64Value", "-1"},
			{"google.protobuf.UInt32Value", "-0"},
		} {
			_, diagnosed := compileCarrier(t, tc[0], tc[1])
			assert.True(t, diagnosed, "%s carrier must reject %s", tc[0], tc[1])
		}
	})

	t.Run("accepted", func(t *testing.T) {
		t.Parallel()
		for _, tc := range [][2]string{
			{"int64", "-1"}, {"int64", "-0"}, {"int32", "-0"},
			{"int64", "-9223372036854775808"}, // MinInt64 negates to itself
			{"double", "-1"}, {"float", "-0"},
			// A float-spelled negative is routed to double_value, so the
			// unsigned arm must not see it either.
			{"uint64", "-1.5"},
		} {
			rep, diagnosed := compileCarrier(t, tc[0], tc[1])
			assert.False(t, diagnosed, "%s must accept %s, got: %v", tc[0], tc[1], rep.Diagnostics)
		}
	})
}

// TestCarrierBoundNamesTheRightType pins what the diagnostic calls the
// bound. A wrapper's bound is the scalar it wraps, and the annotated type
// is the wrapper — naming `int64` as "the annotated type" of a
// `google.protobuf.Int64Value` field is the lie the Unknown branch already
// takes care to avoid for `pxf.BigInt`.
func TestCarrierBoundNamesTheRightType(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ fieldType, lit, want string }{
		{"int64", "1e19", "out of range for the annotated type `int64`"},
		{"google.protobuf.Int64Value", "1e19",
			"out of range for the scalar `int64` wrapped by the annotated type " +
				"`google.protobuf.Int64Value`"},
		{"uint64", "-1", "is negative, but the annotated type `uint64` is unsigned"},
		{"google.protobuf.UInt32Value", "-1",
			"is negative, but the scalar `uint32` wrapped by the annotated type " +
				"`google.protobuf.UInt32Value` is unsigned"},
	} {
		rep, diagnosed := compileCarrier(t, tc.fieldType, tc.lit)
		require.True(t, diagnosed, "%s @deflt(%s) must be diagnosed", tc.fieldType, tc.lit)
		found := false
		for _, d := range rep.Diagnostics {
			if strings.Contains(d.Message(), tc.want) {
				found = true
			}
		}
		assert.True(t, found, "want a diagnostic containing %q, got: %v", tc.want, rep.Diagnostics)
	}
}

// TestCarrierBoundSkipsMembersWithNoElementType pins the third carrier
// state, the one no other test here reaches: a member that HAS no element
// type.
//
// An enum value is such a member — it is yielded by Type.Members() like a
// field, but Element() is zero — so it takes the same route a message- or
// service-level annotation does rather than the unmapped-message route
// that bounds `pxf.BigInt` by int64. That is the documented limit of
// carrier routing (#172) and not something the carrier bound changes; this
// pins which of the two it falls into, since the two differ by a
// diagnostic.
func TestCarrierBoundSkipsMembersWithNoElementType(t *testing.T) {
	t.Parallel()

	_, rep := compileForAnnotationTest(t, `syntax = "proto3";
package test;
annotation deflt(value: any);
enum E {
  E_ZERO = 0;
  E_ONE = 1 @deflt(1e19);
}
`)
	for _, d := range rep.Diagnostics {
		assert.False(t, isError(d),
			"an enum value has no element type to bound against: %v", d.Message())
	}
}
