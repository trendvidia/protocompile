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

// Package expr provides an abstract syntax tree implementation for an
// expression language.
//
// # Status in this fork
//
// Nothing in this repository uses it. It is inherited from upstream's
// `experimental/` tree, promoted to the top level by Track C (#44), and
// upstream is in the same position: bufbuild/protocompile's only importer
// of expr is experimental/internal/exprx, which nothing imports in turn.
// Wiring it to a consumer is upstream's design question, not this fork's.
//
// It said "shared by various parts of the compiler" until #199, which is
// how an audit came to read the package as dead code and propose deleting
// it. It is not dead; it is unfinished, and upstream's.
//
// Two things follow for anyone reading it here. The package is not covered
// by this repository's test suite, so nothing pins the behaviour described
// below. And it is NOT the implementation of RFC-001's `expression`
// parameter type, despite the name: that type is defined as an opaque text
// payload validated by the configured engine at run time, never parsed by
// protocompile (see ir.AnnotationParam.IsExpression). The grammar here —
// `func`, `for ... in`, `break`/`continue`/`return`, and the assignment
// operators — describes a general-purpose imperative language, which is
// more than either a validation predicate or a computed default needs.
//
// # Node Types
//
// Most types in this package represent AST nodes of some kind. Each such type
// contains a grammar for the lenient extension of the Protobuf grammar that it
// represents. The grammar is notated using regular expression syntax, with the
// following modifications:
//  1. Whitespace is ignored.
//  2. Literal strings (except in character classes) are represented with Go
//     strings.
//  3. Unquoted names refer to other productions.
//
// Productions that represent a type from this package, a [token.Kind], or
// a production used by a different type, are in PascalCase; all other
// productions are in camelCase and are scoped to that type.
//
// These grammars are provided for illustration only: the complete grammar is
// ambiguous in the absence of greediness decisions, which, except where
// otherwise noted, neither affect the parsing of correct Protobuf files, nor
// are they specified and are subject to change.
//
// # Pointer-like Types
//
// Virtually all AST nodes in this library are "pointer-like" types, in that
// although they are not Go pointers, they do refer to something stored in a
// [Context] somewhere. Internally, they contain a pointer to a [Context] and
// a pointer to the compressed node representation inside the [Context]. This is
// done so that we can avoid spending an extra eight or twelve bytes per
// node-at-rest, and to minimize GC churn by avoiding pointer cycles in the
// in-memory representation of the AST.
//
// All pointer-like types have a bool-returning IsZero method, which checks for
// the zero value. Pointer-like types should generally be passed by value, not
// by pointer; all of them have value receivers.
package expr

//go:generate go run github.com/trendvidia/protocompile/internal/enum kind.yaml
