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

// Package collide detects Protobuf namespace collisions between modules.
//
// # The failure this prevents
//
// Two Go modules that each vendor a copy of the same `.proto` produce two
// generated packages that both register the same file path and the same
// fully-qualified names into the process-global registry from `init()`.
// Any binary linking both crashes before `main` runs.
//
// Nothing catches this today. Per-module linting operates within a module,
// so each repository is individually clean; the collision exists only in
// the link graph, which no Protobuf tool looks at. The escape hatch is
// worse than the crash — under
// `GOLANG_PROTOBUF_REGISTRATION_CONFLICT=warn` the second registration is
// dropped, so a loud startup crash becomes a silent wrong answer.
//
// # What is checked
//
// [Check] mirrors what `protoregistry.RegisterFile` rejects, because that
// is the function that panics:
//
//   - the file's import path;
//   - each of the file's top-level declarations — messages, enums,
//     extensions and services — by fully-qualified name;
//   - the values of each top-level enum. `rangeTopLevelDescriptors`
//     enumerates them alongside the enum itself, because an enum value is
//     scoped to the enum's parent rather than to the enum: two modules in
//     one Protobuf package whose enums both spell a value `UNSPECIFIED`
//     conflict over that name even though the enums are differently named;
//   - the file's Protobuf package and every ancestor of it, against the
//     other modules' declarations. A package entry and a descriptor cannot
//     share a name, which `RegisterFile` reports as a "package name
//     conflict". Two packages sharing a name is not a conflict, and is not
//     reported.
//
// Nested declarations are not compared separately. A nested name can only
// collide if the top-level name enclosing it already does, and
// `RegisterFile` itself checks only top-level descriptors.
//
// A name claimed twice by a single module is reported too. Nothing else
// catches it: the module's own build does not, because protocompile links
// each file separately and two files in one module may declare the same
// fully-qualified name without error, and `RegisterFile` then panics on
// the second one just as it would across modules.
//
// This is detection. Nothing here changes how generated code registers
// itself, or how `protoregistry` behaves.
package collide

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/trendvidia/protocompile"
)

// Module is one unit whose namespace claims are compared against the others.
type Module struct {
	// Name identifies the module in reports. It is the operator's label —
	// a Go module path or a repository name — and is never interpreted.
	Name string

	// Root is the import root. Import paths inside this module resolve
	// relative to it, so a file at <Root>/pxf/annotations.proto is
	// imported as "pxf/annotations.proto".
	Root string

	// Paths optionally limits which files are checked, relative to Root.
	// When empty every .proto beneath Root is checked.
	Paths []string
}

// Kind distinguishes what was claimed more than once.
type Kind int

const (
	// KindFile is an import path claimed by more than one module. This is
	// `RegisterFile`'s "file %q is already registered".
	KindFile Kind = iota
	// KindSymbol is a fully-qualified name claimed by more than one
	// module. This is `RegisterFile`'s "name conflict over %v".
	KindSymbol
)

func (k Kind) String() string {
	if k == KindFile {
		return "file"
	}
	return "symbol"
}

// Claim records one module's claim on a name.
type Claim struct {
	// Module is the claiming module's Name.
	Module string
	// File is the import path of the .proto that declares it.
	File string
}

// Collision is one name claimed by more than one module.
type Collision struct {
	Kind Kind
	// Name is the import path (KindFile) or fully-qualified name
	// (KindSymbol) that was claimed twice.
	Name string
	// Claims is every claim on Name, ordered by module then file.
	Claims []Claim
}

// String renders a collision as one diagnostic line plus one line per
// claimant.
func (c Collision) String() string {
	var sb strings.Builder
	if mods := c.modules(); len(mods) > 1 {
		fmt.Fprintf(&sb, "%s %q claimed by %d modules", c.Kind, c.Name, len(mods))
	} else {
		// One module claiming the same name from several of its own files
		// panics just the same; saying "1 module" would read as a non-event.
		fmt.Fprintf(&sb, "%s %q claimed by %d files in module %s", c.Kind, c.Name, len(c.Claims), mods[0])
	}
	for _, claim := range c.Claims {
		fmt.Fprintf(&sb, "\n    %s (%s)", claim.Module, claim.File)
	}
	return sb.String()
}

func (c Collision) modules() []string {
	seen := make(map[string]bool, len(c.Claims))
	var out []string
	for _, claim := range c.Claims {
		if !seen[claim.Module] {
			seen[claim.Module] = true
			out = append(out, claim.Module)
		}
	}
	return out
}

// Check compiles each module independently and reports every import path
// and fully-qualified name claimed more than once — by two modules, or by
// two files of one module, both of which panic in
// `protoregistry.RegisterFile`.
//
// A module that cannot be read or compiled, or that holds no `.proto` at
// all, is an error rather than a collision: it has not been cleared, and
// reporting it as clean would be the same silent pass this package exists
// to remove.
//
// The returned collisions are ordered deterministically, so output can be
// compared across runs.
func Check(ctx context.Context, mods []Module) ([]Collision, error) {
	if err := validate(mods); err != nil {
		return nil, err
	}

	// claims[kind][name] accumulates every module that claimed name.
	claims := map[Kind]map[string][]Claim{
		KindFile:   {},
		KindSymbol: {},
	}
	// packages[name] is every claim on name as a Protobuf package. Package
	// claims are held apart from symbol claims because two packages may
	// share a name; only a package meeting a declaration is a conflict.
	packages := map[string][]Claim{}

	for _, mod := range mods {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := collectModule(ctx, mod, importPaths(mod, mods), claims, packages); err != nil {
			return nil, fmt.Errorf("module %s: %w", mod.Name, err)
		}
	}
	mergePackageConflicts(claims[KindSymbol], packages)

	var out []Collision
	for _, kind := range []Kind{KindFile, KindSymbol} {
		for name, cs := range claims[kind] {
			c := Collision{Kind: kind, Name: name, Claims: cs}
			if len(c.Claims) < 2 {
				continue
			}
			sortClaims(c.Claims)
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func validate(mods []Module) error {
	seen := make(map[string]bool, len(mods))
	roots := make(map[string]string, len(mods))
	for _, mod := range mods {
		switch {
		case mod.Name == "":
			return errors.New("collide: module with empty Name")
		case mod.Root == "":
			return fmt.Errorf("collide: module %s has empty Root", mod.Name)
		case seen[mod.Name]:
			return fmt.Errorf("collide: duplicate module name %q", mod.Name)
		}
		seen[mod.Name] = true

		// Two names for one root would report every file in it as claimed
		// twice — a collision that exists only in the arguments.
		key, err := filepath.Abs(mod.Root)
		if err != nil {
			return fmt.Errorf("collide: module %s: %w", mod.Name, err)
		}
		if prev, ok := roots[key]; ok {
			return fmt.Errorf("collide: modules %s and %s have the same root %s", prev, mod.Name, mod.Root)
		}
		roots[key] = mod.Name
	}
	return nil
}

// mergePackageConflicts folds package claims into the symbol claims they
// conflict with. `RegisterFile` keeps packages and descriptors in one
// namespace, so a package entry and a declaration cannot share a name; two
// packages sharing a name is ordinary and stays unreported.
func mergePackageConflicts(symbols, packages map[string][]Claim) {
	for name, pkgClaims := range packages {
		declared, ok := symbols[name]
		if !ok {
			continue
		}
		seen := make(map[Claim]bool, len(declared))
		for _, c := range declared {
			seen[c] = true
		}
		for _, c := range pkgClaims {
			if !seen[c] {
				seen[c] = true
				declared = append(declared, c)
			}
		}
		symbols[name] = declared
	}
}

func sortClaims(cs []Claim) {
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].Module != cs[j].Module {
			return cs[i].Module < cs[j].Module
		}
		return cs[i].File < cs[j].File
	})
}

// importPaths returns the roots to compile mod against: its own first,
// then every other module's.
//
// A module that depends on another imports that other module's files by
// path, and those files are not under its own root — which is the whole
// reason it must not vendor its own copy. Compiling against only its own
// root would fail to resolve exactly the import that makes the constraint
// matter, and report the configuration this checks as unreadable.
//
// Its own root comes first so that a module which *does* vendor a copy
// resolves its own, and therefore claims it. That is what makes the
// duplicate visible rather than silently unifying the two.
func importPaths(mod Module, mods []Module) []string {
	out := make([]string, 0, len(mods))
	out = append(out, mod.Root)
	for _, other := range mods {
		if other.Name != mod.Name {
			out = append(out, other.Root)
		}
	}
	return out
}

// collectModule compiles one module and records what it claims.
func collectModule(
	ctx context.Context,
	mod Module,
	roots []string,
	claims map[Kind]map[string][]Claim,
	packages map[string][]Claim,
) error {
	paths := mod.Paths
	if len(paths) == 0 {
		var err error
		paths, err = discover(mod.Root)
		if err != nil {
			return err
		}
	}
	// A caller-supplied Paths may repeat a file; compiling it twice would
	// report it as claiming its own names twice.
	paths = dedupe(paths)

	compiler := protocompile.Compiler{
		Resolver: &protocompile.SourceResolver{ImportPaths: roots},
	}
	files, err := compiler.Compile(ctx, paths...)
	if err != nil {
		return err
	}

	// Only what this module declares counts. Compile also returns the
	// module's dependencies — the well-known types, and any file pulled in
	// from another module's root — and those are claimed by whoever ships
	// them, not by this module.
	own := make(map[string]bool, len(paths))
	for _, p := range paths {
		own[p] = true
	}

	for _, f := range files {
		path := f.Path()
		if !own[path] {
			continue
		}
		claim := Claim{Module: mod.Name, File: path}
		claims[KindFile][path] = append(claims[KindFile][path], claim)
		for _, name := range topLevelNames(f) {
			claims[KindSymbol][name] = append(claims[KindSymbol][name], claim)
		}
		for _, name := range packageNames(f) {
			packages[name] = append(packages[name], claim)
		}
	}
	return nil
}

// dedupe returns paths with repeats removed, order preserved.
func dedupe(paths []string) []string {
	seen := make(map[string]bool, len(paths))
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// packageNames lists the file's Protobuf package and every ancestor of it,
// which is the set `RegisterFile` walks when it reports a package name
// conflict. A file with no package claims nothing.
func packageNames(fd protoreflect.FileDescriptor) []string {
	var out []string
	for name := fd.Package(); name != ""; name = name.Parent() {
		out = append(out, string(name))
	}
	return out
}

// topLevelNames lists the fully-qualified names of a file's top-level
// declarations — the set `protoregistry.RegisterFile` checks for conflicts,
// as enumerated by its `rangeTopLevelDescriptors`.
func topLevelNames(fd protoreflect.FileDescriptor) []string {
	msgs := fd.Messages()
	enums := fd.Enums()
	exts := fd.Extensions()
	svcs := fd.Services()
	out := make([]string, 0, msgs.Len()+enums.Len()+exts.Len()+svcs.Len())
	for i := range msgs.Len() {
		out = append(out, string(msgs.Get(i).FullName()))
	}
	for i := range enums.Len() {
		enum := enums.Get(i)
		out = append(out, string(enum.FullName()))
		// An enum value is scoped to the enum's parent, not to the enum, so
		// `RegisterFile` registers the values of a top-level enum at the top
		// level too. Two modules whose differently-named enums both spell a
		// value `UNSPECIFIED` in one package conflict over that name.
		vals := enum.Values()
		for j := range vals.Len() {
			out = append(out, string(vals.Get(j).FullName()))
		}
	}
	for i := range exts.Len() {
		out = append(out, string(exts.Get(i).FullName()))
	}
	for i := range svcs.Len() {
		out = append(out, string(svcs.Get(i).FullName()))
	}
	return out
}

// discover lists every .proto beneath root, as slash-separated paths
// relative to it.
//
// A root that yields nothing is an error rather than an empty result. A
// mistyped root that happens to exist, or one that turns out to hold no
// schema at all, has not been cleared, and reporting it as clean would be
// the same silent pass this package exists to remove.
func discover(root string) ([]string, error) {
	// filepath.WalkDir lstats its root, so a symlink to a directory — an
	// ordinary shape for a sibling checkout or a cached module directory —
	// walks as a single non-directory entry and yields nothing at all.
	walkRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("root %s does not exist", root)
		}
		return nil, err
	}
	info, err := os.Stat(walkRoot)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("root %s is not a directory", root)
	}

	var out []string
	err = filepath.WalkDir(walkRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".proto") {
			return nil
		}
		rel, err := filepath.Rel(walkRoot, path)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("root %s contains no .proto files", root)
	}
	sort.Strings(out)
	return out, nil
}
