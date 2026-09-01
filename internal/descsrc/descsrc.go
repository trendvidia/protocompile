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

// Package descsrc renders a [descriptorpb.FileDescriptorProto] back into
// Protobuf source text.
//
// After Track C the compiler pipeline reads source bytes and nothing else,
// so a caller that resolves an import by handing back an already-linked
// descriptor — the `protoregistry.GlobalFiles` pattern — has no way in. This
// package is that way in: the descriptor is rendered to source and fed
// through the ordinary pipeline.
//
// # Fidelity is enforced, not hoped for
//
// The contract is all-or-nothing. [Render] either produces source that
// re-compiles to an equivalent descriptor, or it returns an error naming the
// construct it could not express. It never emits a partial file.
//
// That matters more here than the breadth of what is supported. A renderer
// that quietly dropped a field would turn a loud "imported file does not
// exist" into a silent wrong compile, which is a worse failure than the one
// this package exists to fix. So every construct is either rendered
// faithfully or refused by name, and [ErrUnsupported] is the refusal.
package descsrc

import (
	"errors"
	"fmt"
	"strings"

	"google.golang.org/protobuf/types/descriptorpb"
)

// ErrUnsupported is returned when the descriptor contains a construct this
// package cannot express in source. Errors wrap it, so callers can
// distinguish "this descriptor cannot be rendered" from "this descriptor is
// malformed".
var ErrUnsupported = errors.New("descsrc: unsupported construct")

func unsupportedf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrUnsupported, fmt.Sprintf(format, args...))
}

// malformedf reports a descriptor that is not merely beyond what source can
// express but internally inconsistent — an index pointing past its list, a
// field with no extendee. Such an error deliberately does not wrap
// [ErrUnsupported]: the package's contract is that the wrapping is what
// separates "cannot be rendered" from "is malformed". Render is reachable
// from a caller-supplied descriptor, so neither may be a panic.
func malformedf(format string, args ...any) error {
	return fmt.Errorf("descsrc: malformed descriptor: %s", fmt.Sprintf(format, args...))
}

// Render renders fdp as Protobuf source text.
//
// It returns an error wrapping [ErrUnsupported] if the descriptor contains
// anything it cannot express. It never returns partial output.
func Render(fdp *descriptorpb.FileDescriptorProto) (string, error) {
	if fdp == nil {
		return "", errors.New("descsrc: nil FileDescriptorProto")
	}
	r := &renderer{syntax: fdp.GetSyntax()}
	if r.syntax == "" {
		r.syntax = "proto2"
	}
	if err := r.file(fdp); err != nil {
		// Name the file being rendered; the caller usually knows only the
		// import path that led here.
		return "", fmt.Errorf("descsrc: render %q: %w", fdp.GetName(), err)
	}
	return r.buf.String(), nil
}

type renderer struct {
	buf    strings.Builder
	indent int
	syntax string // "proto2", "proto3", or "editions"
}

func (r *renderer) editions() bool { return r.syntax == "editions" }
func (r *renderer) proto2() bool   { return r.syntax == "proto2" }

// line writes one indented line.
func (r *renderer) linef(format string, args ...any) {
	r.buf.WriteString(strings.Repeat("  ", r.indent))
	if len(args) == 0 {
		r.buf.WriteString(format)
	} else {
		fmt.Fprintf(&r.buf, format, args...)
	}
	r.buf.WriteByte('\n')
}

// blank writes a blank separator line, collapsing runs.
func (r *renderer) blank() {
	s := r.buf.String()
	if s == "" || strings.HasSuffix(s, "\n\n") {
		return
	}
	r.buf.WriteByte('\n')
}

func (r *renderer) file(fdp *descriptorpb.FileDescriptorProto) error {
	switch r.syntax {
	case "proto2", "proto3":
		r.linef("syntax = %s;", quote(r.syntax))
	case "editions":
		if fdp.GetEdition() == descriptorpb.Edition_EDITION_UNKNOWN {
			return unsupportedf("edition syntax with no edition set")
		}
		r.linef("edition = %s;", quote(editionName(fdp.GetEdition())))
	default:
		return unsupportedf("unknown syntax %q", r.syntax)
	}

	// scope is the fully-qualified prefix declarations in this file carry,
	// which is what makes a type_name comparable to a sibling's name.
	scope := ""
	if pkg := fdp.GetPackage(); pkg != "" {
		scope = "." + pkg
		r.blank()
		r.linef("package %s;", pkg)
	}

	if err := r.imports(fdp); err != nil {
		return err
	}

	opts, err := collectOptions(fdp.GetOptions())
	if err != nil {
		return fmt.Errorf("file options: %w", err)
	}
	if len(opts) > 0 {
		r.blank()
		for _, o := range opts {
			r.linef("option %s = %s;", o.name, o.value)
		}
	}

	// A file-scope `extend` block may declare a group, whose body message
	// lands in message_type at the block's own position. So the blocks are
	// emitted where their bodies appear rather than appended at the end, and
	// the messages between two bodies are emitted between the blocks that
	// produce them, or the recompiled message_type comes out reordered.
	// message_type carries the same boundary evidence nested_type does, so
	// file-scope blocks split on it too.
	topIndex := make(map[string]int, len(fdp.GetMessageType()))
	for i, m := range fdp.GetMessageType() {
		topIndex[m.GetName()] = i
	}
	blocks, err := extendBlocks(fdp.GetExtension(),
		func(f *descriptorpb.FieldDescriptorProto) (int, bool) {
			return groupBodyPos(scope, f, topIndex)
		},
	)
	if err != nil {
		return err
	}
	siblings := make(map[string]*descriptorpb.DescriptorProto, len(fdp.GetMessageType()))
	for _, m := range fdp.GetMessageType() {
		siblings[m.GetName()] = m
	}
	producedBy, err := groupBodyAnchors(scope, blocks)
	if err != nil {
		return err
	}
	sched, _, err := scheduleDeclared(fdp.GetMessageType(), producedBy,
		fmt.Sprintf("file %q message_type", fdp.GetName()))
	if err != nil {
		return err
	}
	emit := func(a anchor) error {
		for _, m := range sched[a] {
			r.blank()
			if err := r.message(m, scope); err != nil {
				return err
			}
		}
		return nil
	}
	if err := emit(anchorStart); err != nil {
		return err
	}
	for i, b := range blocks {
		if err := r.extendBlock(scope, b, siblings); err != nil {
			return err
		}
		if err := emit(anchor{extend: true, index: i}); err != nil {
			return err
		}
	}

	for _, e := range fdp.GetEnumType() {
		r.blank()
		if err := r.enum(e); err != nil {
			return err
		}
	}
	for _, s := range fdp.GetService() {
		r.blank()
		if err := r.service(s); err != nil {
			return err
		}
	}
	return nil
}

func (r *renderer) imports(fdp *descriptorpb.FileDescriptorProto) error {
	deps := fdp.GetDependency()
	if len(deps) == 0 {
		return nil
	}
	public := make(map[int32]bool, len(fdp.GetPublicDependency()))
	for _, i := range fdp.GetPublicDependency() {
		public[i] = true
	}
	weak := make(map[int32]bool, len(fdp.GetWeakDependency()))
	for _, i := range fdp.GetWeakDependency() {
		weak[i] = true
	}
	r.blank()
	for i, dep := range deps {
		switch {
		case public[int32(i)]:
			r.linef("import public %s;", quote(dep))
		case weak[int32(i)]:
			r.linef("import weak %s;", quote(dep))
		default:
			r.linef("import %s;", quote(dep))
		}
	}
	return nil
}
