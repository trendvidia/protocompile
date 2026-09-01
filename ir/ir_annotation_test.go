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
