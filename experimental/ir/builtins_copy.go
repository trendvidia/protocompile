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
	"slices"
	"sync"

	"github.com/trendvidia/protocompile/experimental/id"
	"github.com/trendvidia/protocompile/internal/arena"
	"github.com/trendvidia/protocompile/internal/intern"
)

// copyBuiltinFromFallback materialises a single builtin symbol from the
// session's fallback descriptor.proto into `dst`. It is used by
// [resolveBuiltins] to provide stubs for the builtin symbols a user's partial
// vendored descriptor.proto omits.
//
// On success the new symbol is registered in dst.exported under its original
// interned FQN and the returned Ref points at it. If the fallback also lacks
// the symbol (cannot normally happen — the baked-in WKT is complete), a zero
// Ref is returned and [resolveBuiltins] surfaces the original "missing
// required symbol" diagnostic.
//
// The copy is intentionally shallow: only the symbol itself plus its parent
// type's hull is materialised. Sibling members of the parent are not copied
// because the IR machinery only ever indexes builtin parents by FQN — never
// walks their members.
//
// Caller is responsible for re-sorting dst.exported after all desired symbols
// have been copied (the binary-search invariant in [symtab.lookup] requires
// sorted order).
func (s *Session) copyBuiltinFromFallback(dst *File, internedFQN intern.ID, kind SymbolKind) Ref[Symbol] {
	fallback := s.fallbackDescriptorProto()
	if fallback == nil {
		return Ref[Symbol]{}
	}

	srcRef := fallback.exported.lookup(internedFQN)
	srcSym := GetRef(fallback, srcRef)
	if srcSym.IsZero() || srcSym.Kind() != kind {
		return Ref[Symbol]{}
	}

	switch kind {
	case SymbolKindField:
		return s.copyBuiltinMember(dst, fallback, srcSym)
	case SymbolKindMessage, SymbolKindEnum:
		return s.copyBuiltinType(dst, fallback, srcSym)
	default:
		return Ref[Symbol]{}
	}
}

// copyBuiltinMember copies a single [Member] (a `rawMember` + matching
// `rawSymbol`) from the fallback into dst. The Member's parent [Type] is
// materialised in dst on demand via [findOrSynthBuiltinType]; the Member is
// appended to that parent's members list. The Member's element type Ref is
// translated to point at dst's corresponding Type (resolving by FQN and
// recursively copying the type if absent).
func (s *Session) copyBuiltinMember(dst, fallback *File, srcSym Symbol) Ref[Symbol] {
	src := id.Wrap(fallback, id.ID[Member](srcSym.Raw().data))

	parentFQN := src.FullName().Parent()
	parentInterned := dst.session.intern.Intern(string(parentFQN))
	parentType := s.findOrSynthBuiltinType(dst, parentInterned)
	if parentType.IsZero() {
		return Ref[Symbol]{}
	}

	srcRaw := src.Raw()
	newElem := s.translateBuiltinTypeRef(dst, fallback, srcRaw.elem)

	// Value-copy the source rawMember, then rewrite the per-file IDs:
	// - parent: point at dst's parent Type id
	// - elem:   the translated Ref
	// - def, features, options: zero (no AST, no options, no features in dst)
	raw := *srcRaw
	raw.parent = parentType.ID()
	raw.elem = newElem
	raw.def = 0
	raw.features = 0
	raw.options = 0
	raw.featureInfo = nil

	memberID := id.ID[Member](dst.arenas.members.NewCompressed(raw))

	// Append to the parent type's members list. The synthesized parent has
	// only builtin members appended this way; rangeExtnStart and extnsStart
	// stay at 0 (no extension range / no extension fields).
	parentType.Raw().members = append(parentType.Raw().members, memberID)
	parentType.Raw().extnsStart = uint32(len(parentType.Raw().members))

	// Register the new symbol in dst.exported. The kind matches the fallback:
	// non-extension fields land as SymbolKindField. The data slot holds the
	// arena offset of the newly-allocated rawMember.
	symID := dst.arenas.symbols.NewCompressed(rawSymbol{
		kind: SymbolKindField,
		fqn:  src.InternedFullName(),
		data: arena.Untyped(dst.arenas.members.Compress(id.Wrap(dst, memberID).Raw())),
	})
	dst.exported = append(dst.exported, symbol{
		ref: Ref[Symbol]{id: id.ID[Symbol](symID)},
		fqn: src.InternedFullName(),
	})

	return Ref[Symbol]{id: id.ID[Symbol](symID)}
}

// copyBuiltinType materialises a Type stub for a builtin. The stub carries
// only its FQN and parent — its member list is populated incrementally by
// [copyBuiltinMember] as Members are needed. This is sufficient for the IR's
// usage of builtin parent types, which is restricted to FQN lookup and
// Element() chains that resolve back to user-defined types.
func (s *Session) copyBuiltinType(dst, fallback *File, srcSym Symbol) Ref[Symbol] {
	src := id.Wrap(fallback, id.ID[Type](srcSym.Raw().data))
	internedFQN := dst.session.intern.Intern(string(src.FullName()))
	return s.findOrSynthBuiltinType(dst, internedFQN).asSymbolRef(dst)
}

// findOrSynthBuiltinType returns the user-file Type with the given interned
// FQN if it exists, or synthesises a stub Type for it and registers the stub
// in dst.exported. The stub has no members, oneofs, ranges, or AST backing —
// just an FQN, a parent pointer, and an isMessage marker.
//
// Nested types are handled recursively: if the parent FQN names a Type rather
// than a package, the stub is appended to that parent's `nested` list. The
// recursion terminates at a Type that already exists in dst (typically one
// declared in the user's vendored descriptor.proto) or at a package symbol.
//
// Stubs synthesised during the same `resolveBuiltins` pass are cached on
// dst.synthedBuiltins so a second builtin sharing the same parent doesn't
// re-synthesise (and therefore re-register) the parent. `dst.exported` is
// only re-sorted at the end of `resolveBuiltins`, so an unsorted append-only
// region trails the sorted prefix; `dst.exported.lookup` (binary search)
// cannot find symbols inside that region. The cache is the lookup mechanism
// while resolution is in flight.
func (s *Session) findOrSynthBuiltinType(dst *File, internedFQN intern.ID) Type {
	if existing, ok := dst.synthedBuiltins[internedFQN]; ok {
		return existing
	}
	if existing := dst.exported.lookup(internedFQN); !existing.IsZero() {
		sym := GetRef(dst, existing)
		if sym.Kind() == SymbolKindMessage || sym.Kind() == SymbolKindEnum {
			return id.Wrap(dst, id.ID[Type](sym.Raw().data))
		}
	}

	fqn := FullName(dst.session.intern.Value(internedFQN))
	parentFQN := fqn.Parent()
	parentInterned := dst.session.intern.Intern(string(parentFQN))

	// Decide whether this stub is top-level (parent is a package or empty)
	// or nested under another Type. If the parent's symbol resolves to a
	// Type in dst, the stub is added to that Type's `nested` slot;
	// otherwise it is appended to dst.types as a top-level entry.
	var parent Type
	if parentFQN != "" {
		if cached, ok := dst.synthedBuiltins[parentInterned]; ok {
			parent = cached
		} else {
			parentRef := dst.exported.lookup(parentInterned)
			parentSym := GetRef(dst, parentRef)
			switch parentSym.Kind() {
			case SymbolKindMessage, SymbolKindEnum:
				parent = id.Wrap(dst, id.ID[Type](parentSym.Raw().data))
			case SymbolKindPackage:
				// Top-level inside its package.
			default:
				// Parent does not exist yet; synthesise it first.
				parent = s.findOrSynthBuiltinType(dst, parentInterned)
			}
		}
	}

	stub := s.synthBuiltinTypeStub(dst, fqn, parent)
	if dst.synthedBuiltins == nil {
		dst.synthedBuiltins = make(map[intern.ID]Type)
	}
	dst.synthedBuiltins[internedFQN] = stub
	return stub
}

// synthBuiltinTypeStub creates a new minimal Type with the given FQN inside
// dst, registers it under SymbolKindMessage, and returns it. If parent is
// non-zero, the stub is registered as a nested type of parent; otherwise it
// is appended to dst.types as a top-level entry.
func (s *Session) synthBuiltinTypeStub(dst *File, fqn FullName, parent Type) Type {
	fqnID := dst.session.intern.Intern(string(fqn))
	name := dst.session.intern.Intern(fqn.Name())

	raw := rawType{
		fqn:  fqnID,
		name: name,
	}
	if !parent.IsZero() {
		raw.parent = parent.ID()
	}
	typeID := id.ID[Type](dst.arenas.types.NewCompressed(raw))
	ty := id.Wrap(dst, typeID)

	// Stubs need the same lazy memberByName memoizer that lower_walk.go
	// sets on natively-walked types — without it, [Type.MemberByName]
	// panics on the first lookup. `sync.OnceValue` defers the build
	// until after all the [copyBuiltinMember] passes have appended the
	// stub's members.
	ty.Raw().memberByName = sync.OnceValue(ty.makeMembersByName)

	if parent.IsZero() {
		dst.types = slices.Insert(dst.types, dst.topLevelTypesEnd, typeID)
		dst.topLevelTypesEnd++
	} else {
		dst.types = append(dst.types, typeID)
		parent.Raw().nested = append(parent.Raw().nested, typeID)
	}

	symID := dst.arenas.symbols.NewCompressed(rawSymbol{
		kind: SymbolKindMessage,
		fqn:  fqnID,
		data: arena.Untyped(dst.arenas.types.Compress(ty.Raw())),
	})
	dst.exported = append(dst.exported, symbol{
		ref: Ref[Symbol]{id: id.ID[Symbol](symID)},
		fqn: fqnID,
	})

	return ty
}

// translateBuiltinTypeRef rewrites a [Ref[Type]] from the fallback's context
// into dst's context. Predeclared refs (file == -1) pass through unchanged.
// Same-file refs (file == 0 — the fallback file) are resolved by FQN against
// dst; if the type doesn't exist in dst it is synthesised as a stub via
// [findOrSynthBuiltinType]. Imported refs (file > 0) cannot occur for builtin
// symbols (descriptor.proto has no imports) and trip a zero return.
func (s *Session) translateBuiltinTypeRef(dst, fallback *File, srcRef Ref[Type]) Ref[Type] {
	if srcRef.IsZero() {
		return Ref[Type]{}
	}
	if srcRef.file == -1 {
		return srcRef
	}
	if srcRef.file != 0 {
		return Ref[Type]{}
	}

	srcType := id.Wrap(fallback, srcRef.id)
	internedFQN := dst.session.intern.Intern(string(srcType.FullName()))
	dstType := s.findOrSynthBuiltinType(dst, internedFQN)
	return Ref[Type]{id: dstType.ID()}
}

// asSymbolRef returns the Ref[Symbol] for a [Type] by looking it up in its
// containing file's exported table.
func (t Type) asSymbolRef(dst *File) Ref[Symbol] {
	return dst.exported.lookup(t.InternedFullName())
}
