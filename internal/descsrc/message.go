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

func (r *renderer) message(m *descriptorpb.DescriptorProto) error {
	// Map entries are synthesized from a `map<K, V>` field and are rendered
	// as part of that field, never as a message of their own.
	if m.GetOptions().GetMapEntry() {
		return nil
	}

	// A group's body is emitted inline at its field, so the nested message
	// backing it must not also be emitted here. The caller marks those.
	r.linef("message %s {", m.GetName())
	r.indent++
	if err := r.messageBody(m); err != nil {
		return err
	}
	r.indent--
	r.linef("}")
	return nil
}

func (r *renderer) messageBody(m *descriptorpb.DescriptorProto) error {
	opts, err := collectOptions(m.GetOptions())
	if err != nil {
		return fmt.Errorf("message %s options: %w", m.GetName(), err)
	}
	for _, o := range opts {
		r.linef("option %s = %s;", o.name, o.value)
	}

	// Index the nested types so map entries and group bodies can be found
	// and suppressed as standalone messages.
	nested := make(map[string]*descriptorpb.DescriptorProto, len(m.GetNestedType()))
	for _, n := range m.GetNestedType() {
		nested[n.GetName()] = n
	}
	// consumed holds nested types rendered inline (map entries, groups).
	consumed := make(map[string]bool)

	// Synthetic oneofs back proto3 `optional` fields and are never written
	// out; a real oneof groups the fields that name it.
	synthetic := syntheticOneofs(m)

	// Fields belonging to a real oneof are grouped by it.
	oneofFields := make(map[int32][]*descriptorpb.FieldDescriptorProto)
	for _, f := range m.GetField() {
		if f.OneofIndex != nil && !synthetic[f.GetOneofIndex()] {
			i := f.GetOneofIndex()
			oneofFields[i] = append(oneofFields[i], f)
		}
	}

	// nested_type is also declaration order, and two of its entries are
	// synthesized rather than written: a map field creates its `...Entry`
	// message, and a group creates the message holding its body. Both land
	// at the position of the field that produces them, so a nested message
	// declared before a map field must be emitted before it or the
	// recompiled nested_type comes out in a different order.
	//
	// schedule[i] holds the nested messages to emit after field i; -1 holds
	// those that precede every field.
	schedule, err := nestedSchedule(m, nested)
	if err != nil {
		return err
	}
	emitNested := func(after int) error {
		for _, n := range schedule[after] {
			if err := r.message(n); err != nil {
				return err
			}
		}
		return nil
	}
	if err := emitNested(-1); err != nil {
		return err
	}

	// Emit fields in descriptor order, expanding each oneof in place at its
	// first member. The descriptor's field list is declaration order, and a
	// consumer comparing descriptors sees that order — so hoisting oneofs to
	// the end would round-trip to a different descriptor.
	emitted := make(map[int32]bool, len(oneofFields))
	for fieldIdx, f := range m.GetField() {
		if f.OneofIndex != nil && !synthetic[f.GetOneofIndex()] {
			idx := f.GetOneofIndex()
			if emitted[idx] {
				continue
			}
			emitted[idx] = true

			o := m.GetOneofDecl()[idx]
			oopts, err := collectOptions(o.GetOptions())
			if err != nil {
				return fmt.Errorf("oneof %s options: %w", o.GetName(), err)
			}
			r.linef("oneof %s {", o.GetName())
			r.indent++
			for _, oo := range oopts {
				r.linef("option %s = %s;", oo.name, oo.value)
			}
			for _, of := range oneofFields[idx] {
				if err := r.field(of, nested, consumed); err != nil {
					return err
				}
			}
			r.indent--
			r.linef("}")
			if err := emitNested(fieldIdx); err != nil {
				return err
			}
			continue
		}
		if err := r.field(f, nested, consumed); err != nil {
			return err
		}
		if err := emitNested(fieldIdx); err != nil {
			return err
		}
	}

	for _, e := range m.GetEnumType() {
		if err := r.enum(e); err != nil {
			return err
		}
	}

	if err := r.extensionRanges(m); err != nil {
		return err
	}
	r.reservedRanges(m)

	return r.extends(m.GetName(), m.GetExtension(), nested, consumed)
}

// syntheticOneofs reports which oneof indices exist only to back a proto3
// `optional` field. Such a oneof holds exactly one field, and that field
// carries proto3_optional.
func syntheticOneofs(m *descriptorpb.DescriptorProto) map[int32]bool {
	count := make(map[int32]int)
	for _, f := range m.GetField() {
		if f.OneofIndex != nil {
			count[f.GetOneofIndex()]++
		}
	}
	out := make(map[int32]bool)
	for _, f := range m.GetField() {
		if f.GetProto3Optional() && f.OneofIndex != nil && count[f.GetOneofIndex()] == 1 {
			out[f.GetOneofIndex()] = true
		}
	}
	return out
}

func (r *renderer) field(
	f *descriptorpb.FieldDescriptorProto,
	nested map[string]*descriptorpb.DescriptorProto,
	consumed map[string]bool,
) error {
	// A map field is a repeated message field whose message is a synthesized
	// entry type. Recover the `map<K, V>` spelling from the entry's fields.
	if entry, ok := mapEntry(f, nested); ok {
		consumed[entry.GetName()] = true
		key, err := r.typeName(entry.GetField()[0])
		if err != nil {
			return err
		}
		val, err := r.typeName(entry.GetField()[1])
		if err != nil {
			return err
		}
		opts, err := fieldOptions(f)
		if err != nil {
			return err
		}
		r.linef("map<%s, %s> %s = %d%s;", key, val, f.GetName(), f.GetNumber(), opts)
		return nil
	}

	label, err := r.label(f)
	if err != nil {
		return err
	}

	if f.GetType() == descriptorpb.FieldDescriptorProto_TYPE_GROUP {
		body, ok := nested[groupMessageName(f)]
		if !ok {
			return unsupportedf("group field %s has no backing message", f.GetName())
		}
		consumed[body.GetName()] = true
		opts, err := fieldOptions(f)
		if err != nil {
			return err
		}
		r.linef("%sgroup %s = %d%s {", label, body.GetName(), f.GetNumber(), opts)
		r.indent++
		if err := r.messageBody(body); err != nil {
			return err
		}
		r.indent--
		r.linef("}")
		return nil
	}

	typ, err := r.typeName(f)
	if err != nil {
		return err
	}
	opts, err := fieldOptions(f)
	if err != nil {
		return err
	}
	r.linef("%s%s %s = %d%s;", label, typ, f.GetName(), f.GetNumber(), opts)
	return nil
}

// label renders the field's cardinality keyword, including the trailing
// space, or "" when the syntax implies it.
func (r *renderer) label(f *descriptorpb.FieldDescriptorProto) (string, error) {
	switch f.GetLabel() {
	case descriptorpb.FieldDescriptorProto_LABEL_REPEATED:
		return "repeated ", nil
	case descriptorpb.FieldDescriptorProto_LABEL_REQUIRED:
		if !r.proto2() && !r.editions() {
			return "", unsupportedf("required field %s outside proto2", f.GetName())
		}
		return "required ", nil
	case descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL:
		// proto3 writes `optional` only for explicit-presence fields, which
		// are exactly the proto3_optional ones. proto2 always writes it.
		// Editions carry presence in features and write neither.
		switch {
		case r.proto2():
			return "optional ", nil
		case r.editions():
			return "", nil
		case f.GetProto3Optional():
			return "optional ", nil
		default:
			return "", nil
		}
	default:
		return "", unsupportedf("field %s has unknown label %v", f.GetName(), f.GetLabel())
	}
}

// mapEntry reports whether f is a map field, returning the synthesized entry
// message that backs it.
func mapEntry(
	f *descriptorpb.FieldDescriptorProto,
	nested map[string]*descriptorpb.DescriptorProto,
) (*descriptorpb.DescriptorProto, bool) {
	if f.GetLabel() != descriptorpb.FieldDescriptorProto_LABEL_REPEATED ||
		f.GetType() != descriptorpb.FieldDescriptorProto_TYPE_MESSAGE {
		return nil, false
	}
	name := f.GetTypeName()
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		name = name[i+1:]
	}
	entry, ok := nested[name]
	if !ok || !entry.GetOptions().GetMapEntry() || len(entry.GetField()) != 2 {
		return nil, false
	}
	return entry, true
}

// groupMessageName recovers a group's message name from its field name,
// which protoc lowercases.
func groupMessageName(f *descriptorpb.FieldDescriptorProto) string {
	name := f.GetTypeName()
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		name = name[i+1:]
	}
	return name
}

func (r *renderer) typeName(f *descriptorpb.FieldDescriptorProto) (string, error) {
	if scalar, ok := scalarNames[f.GetType()]; ok {
		return scalar, nil
	}
	switch f.GetType() {
	case descriptorpb.FieldDescriptorProto_TYPE_MESSAGE,
		descriptorpb.FieldDescriptorProto_TYPE_ENUM,
		descriptorpb.FieldDescriptorProto_TYPE_GROUP:
		if f.GetTypeName() == "" {
			return "", unsupportedf("field %s has no type name", f.GetName())
		}
		// Descriptors carry fully-qualified names with a leading dot, which
		// is also valid source and resolves unambiguously.
		return f.GetTypeName(), nil
	default:
		return "", unsupportedf("field %s has unknown type %v", f.GetName(), f.GetType())
	}
}

var scalarNames = map[descriptorpb.FieldDescriptorProto_Type]string{
	descriptorpb.FieldDescriptorProto_TYPE_DOUBLE:   "double",
	descriptorpb.FieldDescriptorProto_TYPE_FLOAT:    "float",
	descriptorpb.FieldDescriptorProto_TYPE_INT64:    "int64",
	descriptorpb.FieldDescriptorProto_TYPE_UINT64:   "uint64",
	descriptorpb.FieldDescriptorProto_TYPE_INT32:    "int32",
	descriptorpb.FieldDescriptorProto_TYPE_FIXED64:  "fixed64",
	descriptorpb.FieldDescriptorProto_TYPE_FIXED32:  "fixed32",
	descriptorpb.FieldDescriptorProto_TYPE_BOOL:     "bool",
	descriptorpb.FieldDescriptorProto_TYPE_STRING:   "string",
	descriptorpb.FieldDescriptorProto_TYPE_BYTES:    "bytes",
	descriptorpb.FieldDescriptorProto_TYPE_UINT32:   "uint32",
	descriptorpb.FieldDescriptorProto_TYPE_SFIXED32: "sfixed32",
	descriptorpb.FieldDescriptorProto_TYPE_SFIXED64: "sfixed64",
	descriptorpb.FieldDescriptorProto_TYPE_SINT32:   "sint32",
	descriptorpb.FieldDescriptorProto_TYPE_SINT64:   "sint64",
}

func (r *renderer) extensionRanges(m *descriptorpb.DescriptorProto) error {
	for _, er := range m.GetExtensionRange() {
		opts, err := collectOptions(er.GetOptions())
		if err != nil {
			return fmt.Errorf("extension range options: %w", err)
		}
		suffix := ""
		if len(opts) > 0 {
			parts := make([]string, 0, len(opts))
			for _, o := range opts {
				parts = append(parts, fmt.Sprintf("%s = %s", o.name, o.value))
			}
			suffix = " [" + strings.Join(parts, ", ") + "]"
		}
		r.linef("extensions %s%s;", rangeSpec(er.GetStart(), er.GetEnd(), maxFieldNumber), suffix)
	}
	return nil
}

func (r *renderer) reservedRanges(m *descriptorpb.DescriptorProto) {
	for _, rr := range m.GetReservedRange() {
		r.linef("reserved %s;", rangeSpec(rr.GetStart(), rr.GetEnd(), maxFieldNumber))
	}
	if names := m.GetReservedName(); len(names) > 0 {
		quoted := make([]string, len(names))
		for i, n := range names {
			quoted[i] = quote(n)
		}
		r.linef("reserved %s;", strings.Join(quoted, ", "))
	}
}

// maxFieldNumber is one past the largest legal field number; descriptors
// store exclusive ends, and this one is spelled `max` in source.
const maxFieldNumber = 536870912

// rangeSpec renders a descriptor's [start, end) range in source form, which
// is inclusive and spells the maximum as `max`.
func rangeSpec(start, end, limit int32) string {
	last := end - 1
	switch {
	case last >= limit-1:
		return fmt.Sprintf("%d to max", start)
	case start == last:
		return strconv.Itoa(int(start))
	default:
		return fmt.Sprintf("%d to %d", start, last)
	}
}

// nestedSchedule decides where each explicitly declared nested message must
// be emitted so that recompiling reproduces nested_type in its original
// order.
//
// Map entries and group bodies are synthesized by the field that produces
// them, so they anchor the sequence; everything between two anchors was
// declared there in source.
func nestedSchedule(
	m *descriptorpb.DescriptorProto,
	nested map[string]*descriptorpb.DescriptorProto,
) (map[int][]*descriptorpb.DescriptorProto, error) {
	producedBy := make(map[string]int, len(m.GetField()))
	for i, f := range m.GetField() {
		if entry, ok := mapEntry(f, nested); ok {
			producedBy[entry.GetName()] = i
			continue
		}
		if f.GetType() == descriptorpb.FieldDescriptorProto_TYPE_GROUP {
			producedBy[groupMessageName(f)] = i
		}
	}

	schedule := make(map[int][]*descriptorpb.DescriptorProto)
	last := -1
	for _, n := range m.GetNestedType() {
		if idx, ok := producedBy[n.GetName()]; ok {
			last = idx
			continue
		}
		if n.GetOptions().GetMapEntry() {
			// A map-entry message no field claims cannot be written back:
			// there is no `map<K, V>` spelling that would recreate it.
			return nil, unsupportedf("orphan map entry message %s", n.GetName())
		}
		schedule[last] = append(schedule[last], n)
	}
	return schedule, nil
}
