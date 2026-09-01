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

// extends renders extension fields, grouped by the message they extend so
// each group becomes one `extend` block.
//
// scope names the message the extensions are declared inside, or "" at file
// scope; it is used only for error messages.
func (r *renderer) extends(
	scope string,
	exts []*descriptorpb.FieldDescriptorProto,
	siblings map[string]*descriptorpb.DescriptorProto,
	consumed map[string]bool,
) error {
	if len(exts) == 0 {
		return nil
	}
	// Preserve declaration order of the first extension for each extendee,
	// so output order tracks the descriptor rather than map iteration.
	var order []string
	groups := make(map[string][]*descriptorpb.FieldDescriptorProto)
	for _, f := range exts {
		ext := f.GetExtendee()
		if ext == "" {
			return unsupportedf("extension %s in %q has no extendee", f.GetName(), scope)
		}
		if _, seen := groups[ext]; !seen {
			order = append(order, ext)
		}
		groups[ext] = append(groups[ext], f)
	}

	for _, ext := range order {
		r.blank()
		r.linef("extend %s {", ext)
		r.indent++
		for _, f := range groups[ext] {
			label, err := r.label(f)
			if err != nil {
				return err
			}
			opts, err := fieldOptions(f)
			if err != nil {
				return err
			}

			// An extension is never a map and never sits in a oneof, so the
			// bookkeeping the in-message path needs does not apply here.
			// A group still can appear; its body message lives in the scope
			// enclosing the `extend` block, not inside it.
			if f.GetType() == descriptorpb.FieldDescriptorProto_TYPE_GROUP {
				body, ok := siblings[groupMessageName(f)]
				if !ok {
					return unsupportedf("group extension %s has no backing message", f.GetName())
				}
				consumed[body.GetName()] = true
				r.linef("%sgroup %s = %d%s {", label, body.GetName(), f.GetNumber(), opts)
				r.indent++
				if err := r.messageBody(body); err != nil {
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
	}
	return nil
}
