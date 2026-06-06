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

// FunctionDeclNode represents a function declaration introduced by the
// protowire v1.2 schema extensions (RFC-001).
//
//	function is_e164(value: string);
//	function matches(value: string, pattern: string) [error_code = "common.matches"];
//	function valid_address(msg: Address);
//
// A function declaration is a file-scope construct that names a signature
// for a validation predicate. The body is implemented in the engine
// runtime and bound to the FQN at engine init; this node carries only
// the signature. The return type is contractual `(bool, *Violation)` per
// RFC-001 §6.5 and is not written in the source. Annotation lists
// (trailing @validate / @description / etc.) attach in a follow-up PR.
//
// In protowire v1.2 the keyword "function" is contextual — it is treated
// as a keyword only when it starts a top-level declaration. Everywhere
// else it remains an identifier; see parser/proto.y's identifier and
// *Name productions.
type FunctionDeclNode struct {
	compositeNode
	Keyword    *KeywordNode         // "function"
	Name       *IdentNode           // declared name
	OpenParen  *RuneNode            // "("
	Params     []*FunctionParamNode // zero or more parameters, in source order
	Commas     []*RuneNode          // separators between consecutive Params; len(Commas) == max(0, len(Params)-1)
	CloseParen *RuneNode            // ")"
	Options    *CompactOptionsNode  // optional "[ ... ]" bracket-form options (nil if absent)
	Semicolon  *RuneNode            // ";" (may be nil if parser is recovering from a missing-semicolon error)
}

func (*FunctionDeclNode) fileElement() {}

// NewFunctionDeclNode creates a new *FunctionDeclNode.
//   - keyword: the "function" keyword token
//   - name: the declared name
//   - openParen: the "(" rune
//   - params: parameters in source order; may be empty
//   - commas: separator runes between consecutive params; len(commas) == max(0, len(params)-1)
//   - closeParen: the ")" rune
//   - options: bracket-form options, or nil if none
//   - semicolon: ";" rune; may be nil for parser error recovery
//
// keyword, name, openParen, and closeParen must be non-nil. Each entry in
// params and commas must be non-nil.
func NewFunctionDeclNode(
	keyword *KeywordNode,
	name *IdentNode,
	openParen *RuneNode,
	params []*FunctionParamNode,
	commas []*RuneNode,
	closeParen *RuneNode,
	options *CompactOptionsNode,
	semicolon *RuneNode,
) *FunctionDeclNode {
	if keyword == nil {
		panic("keyword is nil")
	}
	if name == nil {
		panic("name is nil")
	}
	if openParen == nil {
		panic("openParen is nil")
	}
	if closeParen == nil {
		panic("closeParen is nil")
	}
	if len(commas) > 0 && len(commas) != len(params)-1 {
		panic("commas must have length max(0, len(params)-1)")
	}

	numChildren := 3 + len(params) + len(commas) + 1 // keyword, name, openParen, params, commas, closeParen
	if options != nil {
		numChildren++
	}
	if semicolon != nil {
		numChildren++
	}
	children := make([]Node, 0, numChildren)
	children = append(children, keyword, name, openParen)
	for i, param := range params {
		if i > 0 {
			children = append(children, commas[i-1])
		}
		children = append(children, param)
	}
	children = append(children, closeParen)
	if options != nil {
		children = append(children, options)
	}
	if semicolon != nil {
		children = append(children, semicolon)
	}
	return &FunctionDeclNode{
		compositeNode: compositeNode{children: children},
		Keyword:       keyword,
		Name:          name,
		OpenParen:     openParen,
		Params:        params,
		Commas:        commas,
		CloseParen:    closeParen,
		Options:       options,
		Semicolon:     semicolon,
	}
}

// FunctionParamNode represents one parameter in a function declaration:
//
//	value: string
//	pattern: string
//	msg: myco.commons.Address
type FunctionParamNode struct {
	compositeNode
	Name  *IdentNode     // parameter name
	Colon *RuneNode      // ":"
	Type  IdentValueNode // parameter type, e.g. string or myco.commons.Address
}

// NewFunctionParamNode creates a new *FunctionParamNode. All arguments
// must be non-nil.
func NewFunctionParamNode(name *IdentNode, colon *RuneNode, paramType IdentValueNode) *FunctionParamNode {
	if name == nil {
		panic("name is nil")
	}
	if colon == nil {
		panic("colon is nil")
	}
	if paramType == nil {
		panic("paramType is nil")
	}
	children := []Node{name, colon, paramType}
	return &FunctionParamNode{
		compositeNode: compositeNode{children: children},
		Name:          name,
		Colon:         colon,
		Type:          paramType,
	}
}
