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

package fdp

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// DescriptorPath is the parsed form of a source-map entry's
// descriptor_path, per the canonical grammar of RFC-001 §8.3.1:
//
//	descriptorPath   = elementPath , [ annotationAnchor , [ callAnchor ] ] ;
//	elementPath      = [ fqn ] ;
//	annotationAnchor = "[" , fqn , "#" , ordinal , "]" ;
//	callAnchor       = "/arg#" , index , "/call#" , index ;
//
// Examples:
//
//	"myco.User.email"                                    — bare element (TYPE_REFINEMENT)
//	"myco.User.email[protowire.schema.v1.validate#1]"    — second @validate on the field
//	"myco.User[myco.check#0]/arg#0/call#1"               — second call site in the first argument
//
// This type is the shared formatter/parser contract between
// protocompile (producer) and consumers such as protolsp and pxfed —
// consumers never hand-split the string. A path is unique within its
// enclosing SourceMap; cross-file indexes key by
// (SourceMap.file, descriptor_path).
type DescriptorPath struct {
	// Element is the carrier element's canonical fully-qualified name,
	// as protoreflect.FullName renders it, with no leading dot. Enum
	// values use their parent-scoped name ("pkg.OK", not
	// "pkg.Status.OK"). Empty for file-level entries in packageless
	// files.
	Element string

	// Annotation is the fully-qualified name of the anchored
	// annotation, empty when the path has no annotation anchor (the
	// bare-element TYPE_REFINEMENT form). Ordinal is the zero-based
	// index among same-named annotations on the carrier, in
	// AnnotationList order (including rules macro-expanded from type
	// aliases).
	Annotation string
	Ordinal    int

	// HasCall reports whether the path carries a call anchor
	// (FUNCTION_CALL entries only). ArgIndex indexes Annotation.args;
	// CallIndex indexes that argument's Expression.calls.
	HasCall   bool
	ArgIndex  int
	CallIndex int
}

// String renders the path in its canonical form.
func (p DescriptorPath) String() string {
	if p.Annotation == "" {
		return p.Element
	}
	var sb strings.Builder
	sb.WriteString(p.Element)
	sb.WriteByte('[')
	sb.WriteString(p.Annotation)
	sb.WriteByte('#')
	sb.WriteString(strconv.Itoa(p.Ordinal))
	sb.WriteByte(']')
	if p.HasCall {
		sb.WriteString("/arg#")
		sb.WriteString(strconv.Itoa(p.ArgIndex))
		sb.WriteString("/call#")
		sb.WriteString(strconv.Itoa(p.CallIndex))
	}
	return sb.String()
}

// ParseDescriptorPath parses a canonical descriptor_path string. It
// is strict: every shape outside the RFC-001 §8.3.1 grammar is an
// error, so consumers can rely on round-tripping through
// [DescriptorPath.String].
func ParseDescriptorPath(s string) (DescriptorPath, error) {
	var p DescriptorPath

	element, rest, hasAnchor := strings.Cut(s, "[")
	if !isFQN(element, true) {
		return p, fmt.Errorf("fdp: invalid descriptor path %q: element path is not a fully-qualified name", s)
	}
	p.Element = element
	if !hasAnchor {
		return p, nil
	}

	anchor, rest, hasClose := strings.Cut(rest, "]")
	if !hasClose {
		return p, fmt.Errorf("fdp: invalid descriptor path %q: unterminated annotation anchor", s)
	}
	name, ord, hasOrd := strings.Cut(anchor, "#")
	if !hasOrd || !isFQN(name, false) {
		return p, fmt.Errorf("fdp: invalid descriptor path %q: malformed annotation anchor", s)
	}
	n, err := parseDecimal(ord)
	if err != nil {
		return p, fmt.Errorf("fdp: invalid descriptor path %q: %v", s, err)
	}
	p.Annotation = name
	p.Ordinal = n

	if rest == "" {
		return p, nil
	}

	argPart, callPart, hasCall := strings.Cut(rest, "/call#")
	if !hasCall || !strings.HasPrefix(argPart, "/arg#") {
		return p, fmt.Errorf("fdp: invalid descriptor path %q: malformed call anchor", s)
	}
	if p.ArgIndex, err = parseDecimal(strings.TrimPrefix(argPart, "/arg#")); err != nil {
		return p, fmt.Errorf("fdp: invalid descriptor path %q: %v", s, err)
	}
	if p.CallIndex, err = parseDecimal(callPart); err != nil {
		return p, fmt.Errorf("fdp: invalid descriptor path %q: %v", s, err)
	}
	p.HasCall = true
	return p, nil
}

// isFQN reports whether s matches `ident { "." ident }` — a canonical
// FullName with no leading dot. The empty string is legal only when
// allowEmpty (the elementPath of a file-level entry in a packageless
// file).
func isFQN(s string, allowEmpty bool) bool {
	if s == "" {
		return allowEmpty
	}
	for part := range strings.SplitSeq(s, ".") {
		if !isIdent(part) {
			return false
		}
	}
	return true
}

// isIdent reports whether s is a proto identifier:
// `[A-Za-z_][A-Za-z0-9_]*`.
func isIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

// parseDecimal parses a zero-based decimal with no leading zeros and
// no sign, per the §8.3.1 ordinal/index production.
func parseDecimal(s string) (int, error) {
	if s == "" {
		return 0, errors.New("empty ordinal")
	}
	if len(s) > 1 && s[0] == '0' {
		return 0, fmt.Errorf("ordinal %q has a leading zero", s)
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("malformed ordinal %q", s)
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("malformed ordinal %q", s)
	}
	return n, nil
}
