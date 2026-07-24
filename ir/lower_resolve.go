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

package ir

import (
	"fmt"
	"slices"

	"github.com/trendvidia/protocompile/ast"
	"github.com/trendvidia/protocompile/ast/predeclared"
	"github.com/trendvidia/protocompile/ast/syntax"
	"github.com/trendvidia/protocompile/id"
	"github.com/trendvidia/protocompile/internal/ext/iterx"
	"github.com/trendvidia/protocompile/internal/tags"
	"github.com/trendvidia/protocompile/internal/taxa"
	"github.com/trendvidia/protocompile/ir/presence"
	"github.com/trendvidia/protocompile/report"
	"github.com/trendvidia/protocompile/report/rtags"
	"github.com/trendvidia/protocompile/seq"
	"github.com/trendvidia/protocompile/source"
	"github.com/trendvidia/protocompile/token"
	"github.com/trendvidia/protocompile/token/keyword"
)

// resolveNames resolves all of the names that need resolving in a file.
//
// Name resolution always proceeds regardless of builtin validity.
// Field types, method types, and extensions use the symbol table,
// not builtins.
func resolveNames(file *File, r *report.Report) {
	resolveBuiltins(file, r)

	for ty := range seq.Values(file.AllTypes()) {
		if ty.IsMessage() {
			var names syntheticNames
			for field := range seq.Values(ty.Members()) {
				resolveFieldType(field, r)

				// For proto3 sources, we need to resolve the synthetic oneof names for fields with
				// explicit optional presence. See the docs for [Member.SyntheticOneofName] for details.
				if file.syntax == syntax.Proto3 && field.Presence() == presence.Explicit {
					if !field.Oneof().IsZero() {
						continue
					}
					field.Raw().syntheticOneofName = file.session.intern.Intern(
						names.generate(field.Name(), field.Parent()),
					)
				}
			}
		}
	}

	for extend := range seq.Values(file.AllExtends()) {
		resolveExtendeeType(extend, r)
	}

	for field := range seq.Values(file.AllExtensions()) {
		resolveFieldType(field, r)
	}

	for service := range seq.Values(file.Services()) {
		for method := range seq.Values(service.Methods()) {
			resolveMethodTypes(method, r)
		}
	}
}

// resolveFieldType fully resolves the type of a field (extension or otherwise).
func resolveFieldType(field Member, r *report.Report) {
	ty := field.TypeAST()
	var path ast.Path
	kind := presence.Explicit
	switch ty.Kind() {
	case ast.TypeKindPath:
		if field.Context().Syntax() == syntax.Proto3 {
			kind = presence.Implicit
		}
		// NOTE: Editions features are resolved elsewhere, so we default to
		// explicit presence here.

		if field.IsGroup() {
			// Group fields can still have a label, so we check the first prefix, similar to a
			// prefixed non-group field.
			prefix, ok := iterx.First(field.AST().Prefixes())
			if ok {
				switch prefix.Prefix() {
				case keyword.Optional:
					kind = presence.Explicit
				case keyword.Required:
					kind = presence.Required
				case keyword.Repeated:
					kind = presence.Repeated
				}
			}
		}

		path = ty.AsPath().Path

	case ast.TypeKindPrefixed:
		switch ty.AsPrefixed().Prefix() {
		case keyword.Optional:
			kind = presence.Explicit
		case keyword.Required:
			kind = presence.Required
		case keyword.Repeated:
			kind = presence.Repeated
		}

		// Unwrap as many prefixed fields as necessary to get to the bottom
		// of this.
		ty = ty.RemovePrefixes()
		if p := ty.AsPath().Path; !p.IsZero() {
			path = p
			break
		}

		fallthrough

	case ast.TypeKindGeneric:
		// Resolved elsewhere.
		return
	}

	if path.IsZero() {
		// Enum value; this is legalized elsewhere.
		return
	}

	if field.Raw().oneof < 0 {
		field.Raw().oneof = -int32(kind)
	}

	sym := symbolRef{
		File:   field.Context(),
		Report: r,

		span:  path,
		scope: field.Scope(),
		name:  FullName(path.Canonicalized()),

		skipIfNot: isTypeOrAlias,
		accept:    isTypeOrAlias,
		want:      taxa.Type,

		allowScalars:  true,
		suggestImport: true,
	}.resolve()

	// PSE v1 (RFC-001 §5): type aliases are language sugar. A field
	// of alias type lowers to a field of the underlying message /
	// enum / scalar. Walk the alias chain here so the rest of this
	// function sees the resolved base type. The matching annotation
	// propagation runs later, after annotation use sites are
	// resolved — see [propagateTypeAliasAnnotations].
	if sym.Kind() == SymbolKindTypeAlias {
		sym, _, _ = unwrapTypeAlias(field, sym.AsTypeAlias(), path, r)
	}

	if sym.Kind().IsType() {
		ty := sym.AsType()
		field.Raw().elem = ty.toRef(field.Context())

		if mf := sym.AsType().MapField(); !mf.IsZero() {
			r.Errorf("use of synthetic map entry type").Apply(
				report.Snippetf(path, "referenced here"),
				report.Snippetf(mf.TypeAST(), "synthesized by this type"),
				report.Helpf("despite having a user-visible symbol, map entry "+
					"types cannot be used as field types"),
			)
		}

		if !field.Container().MapField().IsZero() && field.Number() == tags.MapEntry_Key {
			// Legalize that the key type must be comparable.
			ty := sym.AsType()
			if !ty.Predeclared().IsMapKey() {
				d := r.Error(errTypeConstraint{
					want: "map key type",
					got:  sym.AsType(),
					decl: field.TypeAST(),
				}).Apply(
					report.Helpf("valid map key types are integer types, `string`, and `bool`"),
				)

				if ty.IsEnum() {
					d.Apply(report.Helpf(
						"counterintuitively, user-defined enum types " +
							"cannot be used as keys"))
				}
			}
		}
	}
}

// isTypeOrAlias accepts the symbol kinds that may appear at a
// field-type position once PSE type aliases are taken into account:
// the underlying message / enum / scalar kinds plus type aliases
// themselves (which the field-type resolver then unwraps via
// [unwrapTypeAlias]).
func isTypeOrAlias(k SymbolKind) bool {
	return k.IsType() || k == SymbolKindTypeAlias
}

// unwrapTypeAlias walks the type-alias chain rooted at `alias` to
// its underlying message / enum / scalar Type symbol. Returns the
// resolved non-alias [Symbol] (zero on cycle) plus the chain's
// accumulated annotation use IDs.
//
// Per RFC-001 §5, type aliases are language sugar: a field of alias
// type lowers to a field of the underlying type, with the alias's
// trailing annotations expanded onto the field's annotation list.
//
// Chains spanning multiple files are fully supported: each link's
// use IDs stay in the alias's own file arena and are returned as
// [Ref]s relative to `field.Context()`, so [Member.Annotations]
// yields them under their defining file's context.
//
// On cycle, emits a diagnostic via `r` and returns a zero Symbol.
// `r` may be nil to suppress diagnostics — the annotation-propagation
// pass relies on this when it re-walks a chain whose diagnostics were
// already emitted during field-type resolution.
//
// The returned slices hold, in base-to-derived order (RFC-001 §6.4:
// rules evaluate base → derived → field-level), each alias link's
// annotation use sites and the link's own [TypeAlias] reference.
// Within one link, uses keep their source order. Each [Ref.file] is
// set relative to `field.Context()`, so the results can be stored
// directly on a [rawMember].
func unwrapTypeAlias(field Member, alias TypeAlias, refSpan source.Spanner, r *report.Report) (Symbol, []Ref[AnnotationUse], []Ref[TypeAlias]) {
	var (
		seen          = map[FullName]bool{}
		linkUses      [][]Ref[AnnotationUse]
		chain         []Ref[TypeAlias]
		current       = alias
		fieldFile     = field.Context()
		markedImports = map[*File]bool{}
	)
	markUsedOnce := func(f *File) {
		if f == fieldFile || markedImports[f] {
			return
		}
		fieldFile.imports.MarkUsed(f)
		markedImports[f] = true
	}
	for {
		fqn := current.FullName()
		if seen[fqn] {
			if r != nil {
				r.Errorf("type alias `%s` is cyclic", fqn).Apply(
					report.Snippetf(refSpan, "referenced here"),
					report.Snippetf(current.AST().Name(), "alias defined here"),
				)
			}
			return Symbol{}, nil, nil
		}
		seen[fqn] = true

		aliasFile := current.Context()
		fileIdx := refFileFor(fieldFile, aliasFile)
		markUsedOnce(aliasFile)

		chain = append(chain, Ref[TypeAlias]{file: fileIdx, id: current.ID()})

		var linkUse []Ref[AnnotationUse]
		for _, useID := range current.Raw().annotationUses {
			linkUse = append(linkUse, Ref[AnnotationUse]{file: fileIdx, id: useID})
		}
		linkUses = append(linkUses, linkUse)

		baseSym := symbolRef{
			File:   aliasFile,
			Report: r,

			span:  current.AST().Value(),
			scope: current.FullName().Parent(),
			name:  FullName(current.BaseTypeName()),

			accept: isTypeOrAlias,
			want:   taxa.Type,

			allowScalars:  true,
			suggestImport: true,
		}.resolve()

		if baseSym.Kind() == SymbolKindTypeAlias {
			current = baseSym.AsTypeAlias()
			continue
		}

		// The walk visits links derived-first (from the name the field
		// was declared with down to the base); flip to base-first,
		// keeping each link's own uses in source order.
		slices.Reverse(chain)
		var uses []Ref[AnnotationUse]
		for i := len(linkUses) - 1; i >= 0; i-- {
			uses = append(uses, linkUses[i]...)
		}
		return baseSym, uses, chain
	}
}

// fieldTypePath extracts the path of a field's declared type, if any.
// Returns a zero path for non-path / prefixed-non-path / generic
// types. This mirrors the extraction logic at the head of
// [resolveFieldType] so the annotation-propagation pass can find the
// alias chain head without re-running the full field-type pipeline.
func fieldTypePath(ty ast.TypeAny) ast.Path {
	switch ty.Kind() {
	case ast.TypeKindPath:
		return ty.AsPath().Path
	case ast.TypeKindPrefixed:
		if stripped := ty.RemovePrefixes(); stripped.Kind() == ast.TypeKindPath {
			return stripped.AsPath().Path
		}
	}
	return ast.Path{}
}

// mapValueTypePath extracts the path of a map-typed field's declared
// value type, if any. Returns a zero path for non-map types and for
// value types that are not plain paths.
func mapValueTypePath(ty ast.TypeAny) ast.Path {
	_, value := ty.RemovePrefixes().AsGeneric().AsMap()
	return fieldTypePath(value)
}

// propagateTypeAliasAnnotations runs after [resolveAnnotationUses]
// and expands a type-alias chain's trailing annotations onto each
// field whose declared type referenced the alias. Per RFC-001 §5,
// `type Email = string @validate(...)` followed by
// `Email email = 1;` is equivalent to
// `string email = 1 @validate(...);`, so the alias chain's
// annotation list appears on the field's annotation list ahead of
// any field-site annotations, in base-to-derived order (RFC-001
// §6.4 evaluation order: base → derived → field-level).
//
// Chains spanning multiple files are supported: alias-side uses
// are stored on the field as cross-file [Ref]s into their defining
// file's arena, and [Member.Annotations] yields them under that
// file's context so [AnnotationUse.AST] and [AnnotationUse.Target]
// stay coherent.
//
// Map fields expand their *value* type's alias chain onto the map
// field itself (issue #109) — per RFC-001 §6.4, `map<K,V>` validates
// per-element using the element's type rules, and engines attribute
// alias-attributed entries as per-element the same way they do for
// `repeated`. The synthetic map-entry value member is skipped so the
// rules lower exactly once. A *key* type's alias chain stays on the
// synthetic key member: expanding it onto the map field would be
// indistinguishable from value rules there and misapply to values.
//
// Cycle and missing-base diagnostics were already emitted during
// [resolveFieldType]; this pass re-walks the chain silently
// (`r == nil`) to collect the now-resolved annotation use IDs.
func propagateTypeAliasAnnotations(file *File) {
	for ty := range seq.Values(file.AllTypes()) {
		if !ty.IsMessage() {
			continue
		}
		isMapEntry := !ty.MapField().IsZero()
		for field := range seq.Values(ty.Members()) {
			if isMapEntry && field.Number() == tags.MapEntry_Value {
				// The value type's rules expand on the map field
				// itself; see above.
				continue
			}
			propagateAliasAnnotationsOn(file, field)
		}
	}
	for ext := range seq.Values(file.AllExtensions()) {
		propagateAliasAnnotationsOn(file, ext)
	}
}

func propagateAliasAnnotationsOn(file *File, field Member) {
	path := fieldTypePath(field.TypeAST())
	if path.IsZero() {
		path = mapValueTypePath(field.TypeAST())
	}
	if path.IsZero() {
		return
	}

	sym := symbolRef{
		File: file,

		span:  path,
		scope: field.Scope(),
		name:  FullName(path.Canonicalized()),

		skipIfNot: isTypeOrAlias,
		accept:    isTypeOrAlias,
		want:      taxa.Type,

		allowScalars: true,
	}.resolve()

	if sym.Kind() != SymbolKindTypeAlias {
		return
	}

	_, aliasUses, aliasChain := unwrapTypeAlias(field, sym.AsTypeAlias(), path, nil)
	if len(aliasChain) == 0 {
		return
	}
	field.Raw().aliasChain = aliasChain
	field.Raw().aliasChainUses = aliasUses
}

func resolveExtendeeType(extend Extend, r *report.Report) {
	path := extend.AST().Name()
	sym := symbolRef{
		File:   extend.Context(),
		Report: r,

		span:  path,
		scope: extend.Scope(),
		name:  FullName(path.Canonicalized()),

		accept: func(k SymbolKind) bool { return k == SymbolKindMessage },
		want:   taxa.MessageType,

		allowScalars:  true,
		suggestImport: true,
	}.resolve()

	if sym.Kind().IsType() {
		extend.Raw().ty = sym.AsType().toRef(extend.Context())
	}
}

func resolveMethodTypes(m Method, r *report.Report) {
	resolve := func(ty ast.TypeAny) (out Ref[Type], stream bool) {
		var path ast.Path
		for path.IsZero() {
			switch ty.Kind() {
			case ast.TypeKindPath:
				path = ty.AsPath().Path
			case ast.TypeKindPrefixed:
				prefixed := ty.AsPrefixed()
				if prefixed.Prefix() == keyword.Stream {
					stream = true
				}
				ty = prefixed.Type()
			default:
				// This is already diagnosed in the parser for us.
				return out, stream
			}
		}

		sym := symbolRef{
			File:   m.Context(),
			Report: r,

			span:  path,
			scope: m.Service().FullName(),
			name:  FullName(path.Canonicalized()),

			accept: func(k SymbolKind) bool { return k == SymbolKindMessage },
			want:   taxa.MessageType,

			allowScalars:  true,
			suggestImport: true,
		}.resolve()

		if sym.Kind().IsType() {
			out = sym.AsType().toRef(m.Context())
		}

		return out, stream
	}

	signature := m.AST().Signature()
	if signature.Inputs().Len() > 0 {
		m.Raw().input, m.Raw().inputStream = resolve(m.AST().Signature().Inputs().At(0))
	}
	if signature.Outputs().Len() > 0 {
		m.Raw().output, m.Raw().outputStream = resolve(m.AST().Signature().Outputs().At(0))
	}
}

// symbolRef is all of the information necessary to resolve a symbol reference.
type symbolRef struct {
	*File
	*report.Report

	scope, name FullName
	span        source.Spanner

	skipIfNot, accept func(SymbolKind) bool
	want              taxa.Noun

	// If true, the names of scalars will be resolved as potential symbols.
	allowScalars bool

	// If true, diagnostics will suggest adding an import.
	suggestImport bool

	// Allow pulling in symbols via import option.
	allowOption bool
}

// resolve performs symbol resolution.
func (r symbolRef) resolve() Symbol {
	var (
		found    Ref[Symbol]
		expected FullName
	)

	var fullResolve bool
	switch {
	case r.name.Absolute():
		if id, ok := r.session.intern.Query(string(r.name.ToRelative())); ok {
			found = r.imported.lookup(id)
			if foundFile := found.Context(r.File); foundFile != r.File {
				r.File.imports.MarkUsed(foundFile)
			}
		}
	case r.allowScalars:
		// TODO: if symbol resolution would provide a different answer for
		// looking up this primitive, we should consider diagnosing it. We don't
		// currently because:
		//
		// 1. Diagnosing every use would be extremely noisy.
		//
		// 2. Diagnosing only the first might be a false positive, which would
		//    make this warning user-hostile.

		prim := predeclared.Lookup(string(r.name))
		if prim.IsScalar() {
			sym := GetRef(r.File, Ref[Symbol]{
				file: -1,
				id:   id.ID[Symbol](prim),
			})
			r.diagnoseLookup(sym, expected)
			return sym
		}

		fallthrough
	default:
		fullResolve = true
		found, expected = r.imported.resolve(r.File, r.scope, r.name, r.skipIfNot, nil)
	}

	sym := GetRef(r.File, found)
	if r.Report != nil {
		d := r.diagnoseLookup(sym, expected)
		if fullResolve && d != nil {
			// Resolve a second time to add debugging information to the diagnostic.
			r.imported.resolve(r.File, r.scope, r.name, r.skipIfNot, d)
		}
	}

	return sym
}

// diagnoseLookup generates diagnostics for a possibly-failed symbol resolution
// operation.
func (r symbolRef) diagnoseLookup(sym Symbol, expectedName FullName) *report.Diagnostic {
	if sym.IsZero() {
		return r.Errorf("cannot find `%s` in this scope", r.name).Apply(
			report.Tag(rtags.UnknownSymbol),
			report.Snippetf(r.span, "not found in this scope"),
			report.Helpf("the full name of this scope is `%s`", r.scope),
		)
	}

	if k := sym.Kind(); r.accept != nil && !r.accept(k) {
		return r.Errorf("expected %s, found %s `%s`", r.want, k.noun(), sym.FullName()).Apply(
			report.Snippetf(r.span, "expected %s", r.want),
			report.Snippetf(sym.Definition(), "defined here"),
		)
	}

	switch {
	case expectedName != "":
		// Complain if we found the "wrong" type.
		return r.Errorf("cannot find `%s` in this scope", r.name).Apply(
			report.Tag(rtags.UnknownSymbol),
			report.Snippetf(r.span, "not found in this scope"),
			report.Snippetf(sym.Definition(),
				"found possibly related symbol `%s`", sym.FullName()),
			report.Notef(
				"Protobuf's name lookup rules expected a symbol `%s`, "+
					"rather than the one we found",
				expectedName),
		)
	case !sym.Visible(r.File, r.allowOption):
		if !r.allowOption && sym.Visible(r.File, true) {
			decl := sym.Import(r.File).Decl
			var option token.Token
			for m := range seq.Values(decl.ModifierTokens()) {
				if m.Keyword() == keyword.Option {
					option = m
				}
			}
			span := source.Join(decl.KeywordToken(), option)

			// This symbol is only visible in option position.
			return r.Errorf("`%s` is only imported for use in options", r.name).Apply(
				report.Snippetf(r.span, "requires non-`option` import"),
				report.Snippetf(decl, "imported as `option` here"),
				report.SuggestEdits(span, "delete `option`", report.Edit{
					Start: 0, End: span.Len(),
					Replace: "import",
				}),
			)
		}

		// Check to see if the corresponding import is visible. If it is, that
		// means that this is an unexported type.
		if imp := sym.Import(r.File); imp.Visible {
			if ty := sym.AsType(); !ty.IsZero() {
				d := r.Errorf("found unexported %s `%s`", ty.noun(), ty.FullName()).Apply(
					report.Snippetf(r.span, "unexported type"),
				)

				// First, see if local was set explicitly.
				var local token.Token
				for prefix := range ty.AST().Type().Prefixes() {
					if prefix.Prefix() == keyword.Local {
						local = prefix.PrefixToken()
						break
					}
				}

				if !local.IsZero() {
					d.Apply(report.Snippetf(local, "marked as local here"))
				} else {
					var span source.Span
					// Otherwise, see if this was set due to a feature.
					if key := ty.Context().builtins().FeatureVisibility; !key.IsZero() {
						feature := ty.FeatureSet().Lookup(key)
						if !feature.IsDefault() {
							span = feature.Value().ValueAST().Span()
						} else {
							span = ty.Context().AST().Syntax().Value().Span()
						}
					}

					d.Apply(report.Snippetf(span, "this implies `local`"))
				}
				return d
			}
		}

		// Complain that we need to import a symbol.
		d := r.Errorf("cannot find `%s` in this scope", r.name).Apply(
			report.Snippetf(r.span, "not visible in this scope"),
			report.Snippetf(sym.Definition(), "found in unimported file"),
		)

		if !r.suggestImport {
			return d
		}

		// Find the last import statement and stick the suggestion after it.
		decls := sym.Context().AST().Decls()
		_, _, imp := iterx.Find2(seq.Backward(decls), func(_ int, d ast.DeclAny) bool {
			return d.Kind() == ast.DeclKindImport
		})

		var offset int
		if !imp.IsZero() {
			offset = imp.Span().End
		}

		replacement := fmt.Sprintf("\nimport %q;", sym.Context().Path())
		if offset == 0 {
			replacement = replacement[1:] + "\n"
		}

		d.Apply(report.SuggestEdits(
			imp.Span().File.Span(offset, offset),
			fmt.Sprintf("bring `%s` into scope", r.name),
			report.Edit{Replace: replacement},
		))

		return d
	}

	return nil
}
