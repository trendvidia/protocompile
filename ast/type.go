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

package ast

// TypeDeclNode represents a type-alias declaration introduced by the
// protowire v1.2 schema extensions (RFC-001).
//
//	type Email = string;
//
// A type declaration is a file-scope construct that names a refinement
// of a base type. The base type may be a primitive, an enum, a wrapper,
// a message, or another type declaration. Refinement rules (e.g.,
// @validate(...)) attach via a trailing annotation list that is added
// in a follow-up PR; this node carries only the base form.
//
// In protowire v1.2 the keyword "type" is contextual — it is treated as
// a keyword only when it starts a top-level declaration. Everywhere
// else (oneof names, field names, etc.) it remains an identifier; see
// parser/proto.y's identifier and *Name productions.
type TypeDeclNode struct {
	compositeNode
	Keyword   *KeywordNode   // "type"
	Name      *IdentNode     // declared name, e.g. Email
	Equals    *RuneNode      // "="
	BaseType  IdentValueNode // base type reference, e.g. string or myco.commons.Email
	Semicolon *RuneNode      // ";"
}

func (*TypeDeclNode) fileElement() {}

// NewTypeDeclNode creates a new *TypeDeclNode.
//   - keyword: the "type" keyword token
//   - name: the declared name
//   - equals: the "=" rune token
//   - baseType: the base type (a single or qualified identifier value node)
//   - semicolon: the ";" rune token (may be nil if the parser is recovering from
//     a missing-semicolon error; callers wishing to require a semicolon should
//     gate on it being non-nil before invoking)
//
// keyword, name, equals, and baseType must be non-nil.
func NewTypeDeclNode(keyword *KeywordNode, name *IdentNode, equals *RuneNode, baseType IdentValueNode, semicolon *RuneNode) *TypeDeclNode {
	if keyword == nil {
		panic("keyword is nil")
	}
	if name == nil {
		panic("name is nil")
	}
	if equals == nil {
		panic("equals is nil")
	}
	if baseType == nil {
		panic("baseType is nil")
	}
	numChildren := 4
	if semicolon != nil {
		numChildren++
	}
	children := make([]Node, 0, numChildren)
	children = append(children, keyword, name, equals, baseType)
	if semicolon != nil {
		children = append(children, semicolon)
	}
	return &TypeDeclNode{
		compositeNode: compositeNode{children: children},
		Keyword:       keyword,
		Name:          name,
		Equals:        equals,
		BaseType:      baseType,
		Semicolon:     semicolon,
	}
}
