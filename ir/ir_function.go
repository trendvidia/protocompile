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
	"github.com/trendvidia/protocompile/ast"
	"github.com/trendvidia/protocompile/id"
	"github.com/trendvidia/protocompile/internal/intern"
	"github.com/trendvidia/protocompile/seq"
)

// Function is a `function` declaration introduced by the Protowire
// Schema Extensions (PSE) grammar — see [ast.DeclFunction].
//
// Function declarations name an external function callable from
// inside `expression` arguments at annotation use sites. They live
// in the file's exported symbol table under [SymbolKindFunction] so
// cross-file resolution works the same way it does for `message`,
// `service`, and `annotation`.
//
// Phase scope: symbol registration plus FDP emission via the
// [pwsv1.FileFunctions] file-scope carrier. Argument validation
// (verifying that a use-site call's arity matches the declared
// signature) requires the engine-language parser; the engine itself
// performs that check at runtime.
type Function id.Node[Function, *File, *rawFunction]

// FunctionParam is a single parameter of a [Function] declaration —
// see [ast.DeclFunctionParam].
type FunctionParam id.Node[FunctionParam, *File, *rawFunctionParam]

type rawFunction struct {
	def       id.ID[ast.DeclFunction]
	fqn, name intern.ID
	params    []id.ID[FunctionParam]

	annotationUses []id.ID[AnnotationUse]
}

type rawFunctionParam struct {
	def      id.ID[ast.DeclFunctionParam]
	parent   id.ID[Function]
	name     intern.ID
	typeName intern.ID
}

// AST returns the declaration for this function, if known.
func (f Function) AST() ast.DeclFunction {
	if f.IsZero() {
		return ast.DeclFunction{}
	}
	return id.Wrap(f.Context().AST(), f.Raw().def)
}

// Name returns this function's declared name, i.e. the last
// component of its full name.
func (f Function) Name() string {
	return f.FullName().Name()
}

// FullName returns this function's fully-qualified name.
func (f Function) FullName() FullName {
	if f.IsZero() {
		return ""
	}
	return FullName(f.Context().session.intern.Value(f.Raw().fqn))
}

// InternedName returns the intern ID for [Function.FullName]().Name().
func (f Function) InternedName() intern.ID {
	if f.IsZero() {
		return 0
	}
	return f.Raw().name
}

// InternedFullName returns the intern ID for [Function.FullName].
func (f Function) InternedFullName() intern.ID {
	if f.IsZero() {
		return 0
	}
	return f.Raw().fqn
}

// Params returns the parameters of this function declaration.
func (f Function) Params() seq.Indexer[FunctionParam] {
	var params []id.ID[FunctionParam]
	if !f.IsZero() {
		params = f.Raw().params
	}
	return seq.NewFixedSlice(
		params,
		func(_ int, p id.ID[FunctionParam]) FunctionParam {
			return id.Wrap(f.Context(), p)
		},
	)
}

// FunctionOption is one bracket-form option attached to a [Function]
// declaration site, e.g. `error_code = "common.matches.failed"` in
// `function matches(...) [error_code = "..."];`.
type FunctionOption struct {
	// Name is the unqualified option name: the last component of the
	// path the option was written with. The FunctionDecl descriptor
	// carrier keys its options map by this name.
	Name string
	// Value is the option's value expression, as written.
	Value ast.ExprAny
}

// Options returns the bracket-form options attached to this function
// declaration, in declaration order.
//
// Like parameter types, option values are recorded without
// classification: the descriptor surface carries them
// AnnotationArg-shaped and consumers interpret them at use time.
func (f Function) Options() seq.Indexer[FunctionOption] {
	entries := f.AST().Options().Entries()
	return seq.NewFunc(entries.Len(), func(i int) FunctionOption {
		opt := entries.At(i)
		var name string
		for c := range opt.Path.Components() {
			if tok := c.AsIdent(); !tok.IsZero() {
				name = tok.Text()
			}
		}
		return FunctionOption{Name: name, Value: opt.Value}
	})
}

// Annotations returns the annotation use sites attached to this
// function declaration (trailing form: `function foo() @bar;`).
func (f Function) Annotations() seq.Indexer[AnnotationUse] {
	if f.IsZero() {
		return annotationUses(nil, nil)
	}
	return annotationUses(f.Context(), f.Raw().annotationUses)
}

// AST returns the declaration for this function parameter, if known.
func (p FunctionParam) AST() ast.DeclFunctionParam {
	if p.IsZero() {
		return ast.DeclFunctionParam{}
	}
	return id.Wrap(p.Context().AST(), p.Raw().def)
}

// Name returns this parameter's declared name.
func (p FunctionParam) Name() string {
	if p.IsZero() {
		return ""
	}
	return p.Context().session.intern.Value(p.Raw().name)
}

// InternedName returns the intern ID for [FunctionParam.Name].
func (p FunctionParam) InternedName() intern.ID {
	if p.IsZero() {
		return 0
	}
	return p.Raw().name
}

// TypeName returns the textual type the parameter was declared with
// (e.g. "string", "any", or a user-defined type FQN). Stored as the
// raw path text without further classification — engine-language
// type checks happen at runtime.
func (p FunctionParam) TypeName() string {
	if p.IsZero() || p.Raw().typeName == 0 {
		return ""
	}
	return p.Context().session.intern.Value(p.Raw().typeName)
}

// Function returns the function declaration that this parameter
// belongs to.
func (p FunctionParam) Function() Function {
	if p.IsZero() {
		return Function{}
	}
	return id.Wrap(p.Context(), p.Raw().parent)
}
