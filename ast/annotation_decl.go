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

// AnnotationDeclNode represents an annotation declaration introduced by
// the protowire v1.2 schema extensions (RFC-001).
//
//	annotation required;
//	annotation description(text: string);
//	annotation validate(rule: expression, code: string = "", message: string = "");
//	annotation example(value: any);
//
// An annotation declaration is a file-scope construct that declares a
// named metadata attachment usable at use sites via the `@name(args)`
// syntax. Use sites attach in a follow-up PR (PR 5).
//
// The parameter list is optional in the source: `annotation required;`
// (no parens) and `annotation required();` (empty parens) are both
// accepted forms. When parens are absent, OpenParen and CloseParen are
// nil. When parens are present but empty, Params is empty but OpenParen
// and CloseParen are non-nil.
//
// In protowire v1.2 the keyword "annotation" is contextual — recognized
// as a keyword only when it starts a top-level declaration.
type AnnotationDeclNode struct {
	compositeNode
	Keyword    *KeywordNode           // "annotation"
	Name       *IdentNode             // declared name
	OpenParen  *RuneNode              // "(" or nil if absent
	Params     []*AnnotationParamNode // zero or more parameters, in source order
	Commas     []*RuneNode            // separators between consecutive Params; len(Commas) == max(0, len(Params)-1)
	CloseParen *RuneNode              // ")" or nil if absent
	Semicolon  *RuneNode              // ";" (may be nil for parser error recovery)
}

func (*AnnotationDeclNode) fileElement() {}

// NewAnnotationDeclNode creates a new *AnnotationDeclNode.
//   - keyword: the "annotation" keyword token (required)
//   - name: the declared name (required)
//   - openParen: "(" rune, or nil if the source omits the parameter list entirely
//   - params: parameters in source order; may be empty
//   - commas: separator runes; len(commas) == max(0, len(params)-1)
//   - closeParen: ")" rune; must be nil if openParen is nil, non-nil otherwise
//   - semicolon: ";" rune; may be nil for parser error recovery
//
// openParen and closeParen must be both nil or both non-nil.
func NewAnnotationDeclNode(
	keyword *KeywordNode,
	name *IdentNode,
	openParen *RuneNode,
	params []*AnnotationParamNode,
	commas []*RuneNode,
	closeParen *RuneNode,
	semicolon *RuneNode,
) *AnnotationDeclNode {
	if keyword == nil {
		panic("keyword is nil")
	}
	if name == nil {
		panic("name is nil")
	}
	if (openParen == nil) != (closeParen == nil) {
		panic("openParen and closeParen must both be nil or both non-nil")
	}
	if openParen == nil && len(params) > 0 {
		panic("params present but openParen is nil")
	}
	if len(commas) > 0 && len(commas) != len(params)-1 {
		panic("commas must have length max(0, len(params)-1)")
	}

	numChildren := 2 // keyword, name
	if openParen != nil {
		numChildren += 2 + len(params) + len(commas) // openParen, params, commas, closeParen
	}
	if semicolon != nil {
		numChildren++
	}
	children := make([]Node, 0, numChildren)
	children = append(children, keyword, name)
	if openParen != nil {
		children = append(children, openParen)
		for i, param := range params {
			if i > 0 {
				children = append(children, commas[i-1])
			}
			children = append(children, param)
		}
		children = append(children, closeParen)
	}
	if semicolon != nil {
		children = append(children, semicolon)
	}
	return &AnnotationDeclNode{
		compositeNode: compositeNode{children: children},
		Keyword:       keyword,
		Name:          name,
		OpenParen:     openParen,
		Params:        params,
		Commas:        commas,
		CloseParen:    closeParen,
		Semicolon:     semicolon,
	}
}

// AnnotationParamNode represents one parameter in an annotation declaration:
//
//	rule: expression
//	code: string = ""
//	value: any
//
// The default value (Equals and Default) is optional. When absent, both
// are nil; when present, both are non-nil.
//
// The Type field holds an IdentValueNode that may resolve to one of the
// reserved parameter-type designators in the spec (`expression`,
// `string`, `int32`, `int64`, `float`, `double`, `bool`, `bytes`, `any`)
// or to a qualified message- or enum-type name (e.g.
// `myco.commons.Address`). The parser treats it as a qualified identifier;
// semantic interpretation happens at the linker.
type AnnotationParamNode struct {
	compositeNode
	Name    *IdentNode     // parameter name
	Colon   *RuneNode      // ":"
	Type    IdentValueNode // parameter type
	Equals  *RuneNode      // "=" or nil if no default
	Default ValueNode      // default value, or nil if no default
}

// NewAnnotationParamNode creates a new *AnnotationParamNode.
//   - name, colon, paramType: required
//   - equals, defaultValue: both nil if no default; both non-nil if a default is present
func NewAnnotationParamNode(name *IdentNode, colon *RuneNode, paramType IdentValueNode, equals *RuneNode, defaultValue ValueNode) *AnnotationParamNode {
	if name == nil {
		panic("name is nil")
	}
	if colon == nil {
		panic("colon is nil")
	}
	if paramType == nil {
		panic("paramType is nil")
	}
	if (equals == nil) != (defaultValue == nil) {
		panic("equals and defaultValue must both be nil or both non-nil")
	}

	numChildren := 3
	if equals != nil {
		numChildren += 2
	}
	children := make([]Node, 0, numChildren)
	children = append(children, name, colon, paramType)
	if equals != nil {
		children = append(children, equals, defaultValue)
	}
	return &AnnotationParamNode{
		compositeNode: compositeNode{children: children},
		Name:          name,
		Colon:         colon,
		Type:          paramType,
		Equals:        equals,
		Default:       defaultValue,
	}
}
