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

package descsrc

import (
	"fmt"
	"strconv"
	"strings"

	"google.golang.org/protobuf/types/descriptorpb"
)

func (r *renderer) enum(e *descriptorpb.EnumDescriptorProto) error {
	r.linef("enum %s {", e.GetName())
	r.indent++

	opts, err := collectOptions(e.GetOptions())
	if err != nil {
		return fmt.Errorf("enum %s options: %w", e.GetName(), err)
	}
	for _, o := range opts {
		r.linef("option %s = %s;", o.name, o.value)
	}

	for _, v := range e.GetValue() {
		vopts, err := collectOptions(v.GetOptions())
		if err != nil {
			return fmt.Errorf("enum value %s options: %w", v.GetName(), err)
		}
		r.linef("%s = %d%s;", v.GetName(), v.GetNumber(), bracketOptions(vopts))
	}

	for _, rr := range e.GetReservedRange() {
		r.linef("reserved %s;", enumRangeSpec(rr.GetStart(), rr.GetEnd()))
	}
	if names := e.GetReservedName(); len(names) > 0 {
		quoted := make([]string, len(names))
		for i, n := range names {
			quoted[i] = quote(n)
		}
		r.linef("reserved %s;", strings.Join(quoted, ", "))
	}

	r.indent--
	r.linef("}")
	return nil
}

// enumRangeSpec renders an enum reserved range.
//
// EnumDescriptorProto.EnumReservedRange is inclusive on both ends, unlike
// DescriptorProto's reserved and extension ranges, which are half-open.
// Treating the two alike silently shifts every enum reservation by one.
func enumRangeSpec(start, end int32) string {
	switch {
	case end >= maxEnumNumber:
		return fmt.Sprintf("%d to max", start)
	case start == end:
		return strconv.Itoa(int(start))
	default:
		return fmt.Sprintf("%d to %d", start, end)
	}
}

// maxEnumNumber is the largest enum value, spelled `max` in source.
const maxEnumNumber = 2147483647

func (r *renderer) service(s *descriptorpb.ServiceDescriptorProto) error {
	r.linef("service %s {", s.GetName())
	r.indent++

	opts, err := collectOptions(s.GetOptions())
	if err != nil {
		return fmt.Errorf("service %s options: %w", s.GetName(), err)
	}
	for _, o := range opts {
		r.linef("option %s = %s;", o.name, o.value)
	}

	for _, m := range s.GetMethod() {
		in := streamPrefix(m.GetClientStreaming()) + m.GetInputType()
		out := streamPrefix(m.GetServerStreaming()) + m.GetOutputType()

		mopts, err := collectOptions(m.GetOptions())
		if err != nil {
			return fmt.Errorf("method %s options: %w", m.GetName(), err)
		}
		if len(mopts) == 0 {
			r.linef("rpc %s(%s) returns (%s);", m.GetName(), in, out)
			continue
		}
		r.linef("rpc %s(%s) returns (%s) {", m.GetName(), in, out)
		r.indent++
		for _, o := range mopts {
			r.linef("option %s = %s;", o.name, o.value)
		}
		r.indent--
		r.linef("}")
	}

	r.indent--
	r.linef("}")
	return nil
}

func streamPrefix(streaming bool) string {
	if streaming {
		return "stream "
	}
	return ""
}

// extendBlock is one `extend` block: a run of consecutive entries in a
// descriptor's extension list that share an extendee.
type extendBlock struct {
	extendee string
	fields   []*descriptorpb.FieldDescriptorProto
}

// extendBlocks splits an extension list back into the source blocks that
// wrote it.
//
// The split is on consecutive runs, not on the extendee overall. The list is
// declaration order, so `extend A {a} extend B {b} extend A {c}` stores
// [a, b, c]; folding the two A blocks into one would emit [a, c, b] and
// round-trip to a different descriptor.
// extendBlocks groups extensions into the `extend` blocks that wrote them.
//
// A descriptor does not record block boundaries — three `extend Foo` blocks
// and one holding the same three extensions produce an identical extension
// list. What does distinguish them is nested_type: a block declaring a
// group puts that group's body at the block's own position, so a field's
// entry appearing between two group bodies proves a block boundary there.
//
// splitAt reports whether a new block must start before the extension whose
// group body sits at the given nested_type position, given the previous
// one's. It is nil at file scope and wherever no such evidence exists, in
// which case consecutive same-extendee extensions fold into one block —
// which round-trips identically, since the difference is unobservable.
func extendBlocks(
	exts []*descriptorpb.FieldDescriptorProto,
	bodyPos func(*descriptorpb.FieldDescriptorProto) (int, bool),
	fieldBetween func(prev, cur int) bool,
) ([]extendBlock, error) {
	var out []extendBlock
	prevBody := -1
	for _, f := range exts {
		extendee := f.GetExtendee()
		if extendee == "" {
			return nil, malformedf("extension %s has no extendee", f.GetName())
		}

		split := false
		cur := -1
		if bodyPos != nil {
			if pos, ok := bodyPos(f); ok {
				cur = pos
				if prevBody >= 0 && fieldBetween(prevBody, pos) {
					split = true
				}
			}
		}

		if n := len(out); n > 0 && out[n-1].extendee == extendee && !split {
			out[n-1].fields = append(out[n-1].fields, f)
		} else {
			out = append(out, extendBlock{
				extendee: extendee,
				fields:   []*descriptorpb.FieldDescriptorProto{f},
			})
		}
		if cur >= 0 {
			prevBody = cur
		}
	}
	return out, nil
}

// extendBlock renders one `extend` block.
//
// scope is the fully-qualified name of the declaration the block sits
// inside, and siblings the messages declared alongside it: a group
// extension's body message lives in the scope enclosing the block, not
// inside it.
func (r *renderer) extendBlock(
	scope string,
	b extendBlock,
	siblings map[string]*descriptorpb.DescriptorProto,
) error {
	r.blank()
	r.linef("extend %s {", b.extendee)
	r.indent++
	for _, f := range b.fields {
		label, err := r.label(f)
		if err != nil {
			return err
		}
		opts, err := fieldOptions(f)
		if err != nil {
			return err
		}

		// An extension is never a map and never sits in a oneof, so the
		// bookkeeping the in-message path needs does not apply here. A group
		// still can appear.
		if f.GetType() == descriptorpb.FieldDescriptorProto_TYPE_GROUP {
			body, err := groupBody(f, scope, siblings)
			if err != nil {
				return err
			}
			r.linef("%sgroup %s = %d%s {", label, body.GetName(), f.GetNumber(), opts)
			r.indent++
			if err := r.messageBody(body, scope+"."+body.GetName()); err != nil {
				return err
			}
			r.indent--
			r.linef("}")
			continue
		}

		typ, err := r.typeName(f)
		if err != nil {
			return err
		}
		r.linef("%s%s %s = %d%s;", label, typ, f.GetName(), f.GetNumber(), opts)
	}
	r.indent--
	r.linef("}")
	return nil
}
