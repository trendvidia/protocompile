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

// message renders m as a `message` declaration. scope is the
// fully-qualified name of the declaration m sits inside, without a trailing
// dot — "" at file scope in a package-less file.
func (r *renderer) message(m *descriptorpb.DescriptorProto, scope string) error {
	// Map entries are synthesized from a `map<K, V>` field and are rendered
	// as part of that field, never as a message of their own. Reaching here
	// with one means no field claimed it, and there is no `map<K, V>`
	// spelling that would recreate it — refusing is the contract, dropping
	// it silently is what the contract exists to prevent.
	if m.GetOptions().GetMapEntry() {
		return unsupportedf("map entry message %s is produced by no field", m.GetName())
	}

	r.linef("message %s {", m.GetName())
	r.indent++
	if err := r.messageBody(m, scope+"."+m.GetName()); err != nil {
		return err
	}
	r.indent--
	r.linef("}")
	return nil
}

// messageBody renders the contents of a message declaration. scope is the
// message's own fully-qualified name; a group's body is a message too, and
// reaches here under the name the group declares.
func (r *renderer) messageBody(m *descriptorpb.DescriptorProto, scope string) error {
	opts, err := collectOptions(m.GetOptions())
	if err != nil {
		return fmt.Errorf("message %s options: %w", m.GetName(), err)
	}
	for _, o := range opts {
		r.linef("option %s = %s;", o.name, o.value)
	}

	// Index the nested types so map entries and group bodies can be found
	// and rendered at the declaration that synthesized them.
	nested := make(map[string]*descriptorpb.DescriptorProto, len(m.GetNestedType()))
	for _, n := range m.GetNestedType() {
		nested[n.GetName()] = n
	}

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

	blocks, err := extendBlocks(m.GetExtension())
	if err != nil {
		return err
	}

	// nested_type is also declaration order, and some of its entries are
	// synthesized rather than written: a map field creates its `...Entry`
	// message, and a group — whether a field or an extension — creates the
	// message holding its body. Each lands at the position of the
	// declaration that produces it, so those declarations anchor the
	// sequence and a nested message declared between two of them must be
	// emitted between them.
	producedBy, err := fieldAnchors(scope, m.GetField(), nested)
	if err != nil {
		return err
	}
	blockAnchors, err := groupBodyAnchors(scope, blocks)
	if err != nil {
		return err
	}
	for name, a := range blockAnchors {
		producedBy[name] = a
	}
	sched, err := scheduleDeclared(m.GetNestedType(), producedBy,
		fmt.Sprintf("message %s nested_type", m.GetName()))
	if err != nil {
		return err
	}
	emitNested := func(a anchor) error {
		for _, n := range sched[a] {
			if err := r.message(n, scope); err != nil {
				return err
			}
		}
		return nil
	}
	if err := emitNested(anchorStart); err != nil {
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
				// The oneof already went out at its first member. This
				// member may still be a group, and so still anchor a nested
				// message, which is why the schedule is flushed here too.
				if err := emitNested(anchor{index: fieldIdx}); err != nil {
					return err
				}
				continue
			}
			emitted[idx] = true

			if int(idx) >= len(m.GetOneofDecl()) {
				return malformedf("field %s names oneof index %d but message %s declares %d",
					f.GetName(), idx, m.GetName(), len(m.GetOneofDecl()))
			}
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
				if err := r.field(of, scope, nested); err != nil {
					return err
				}
			}
			r.indent--
			r.linef("}")
			if err := emitNested(anchor{index: fieldIdx}); err != nil {
				return err
			}
			continue
		}
		if err := r.field(f, scope, nested); err != nil {
			return err
		}
		if err := emitNested(anchor{index: fieldIdx}); err != nil {
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

	for i, b := range blocks {
		if err := r.extendBlock(scope, b, nested); err != nil {
			return err
		}
		if err := emitNested(anchor{extend: true, index: i}); err != nil {
			return err
		}
	}
	return nil
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
	scope string,
	nested map[string]*descriptorpb.DescriptorProto,
) error {
	// A map field is a repeated message field whose message is a synthesized
	// entry type. Recover the `map<K, V>` spelling from the entry's fields.
	if entry, ok := mapEntry(f, scope, nested); ok {
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
		body, err := groupBody(f, scope, nested)
		if err != nil {
			return err
		}
		opts, err := fieldOptions(f)
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

// localName returns the simple name typeName refers to when the type it
// names is declared directly in scope, and reports whether it is.
//
// The comparison has to be on the whole qualified name. Matching on the last
// dot-segment alone confuses `.Other.FooEntry` with a sibling `FooEntry`,
// and a field whose type merely ends in the same short name as a sibling map
// entry would then render as a map — the silent wrong compile this package
// exists to rule out.
func localName(scope, typeName string) (string, bool) {
	rest, ok := strings.CutPrefix(typeName, scope+".")
	if !ok || rest == "" || strings.Contains(rest, ".") {
		return "", false
	}
	return rest, true
}

// mapEntry reports whether f is a map field, returning the synthesized entry
// message that backs it.
func mapEntry(
	f *descriptorpb.FieldDescriptorProto,
	scope string,
	nested map[string]*descriptorpb.DescriptorProto,
) (*descriptorpb.DescriptorProto, bool) {
	if f.GetLabel() != descriptorpb.FieldDescriptorProto_LABEL_REPEATED ||
		f.GetType() != descriptorpb.FieldDescriptorProto_TYPE_MESSAGE {
		return nil, false
	}
	name, ok := localName(scope, f.GetTypeName())
	if !ok {
		return nil, false
	}
	entry, ok := nested[name]
	if !ok || !entry.GetOptions().GetMapEntry() || len(entry.GetField()) != 2 {
		return nil, false
	}
	return entry, true
}

// groupBody returns the message holding a group's body, which is always
// declared in the scope the group itself is declared in.
func groupBody(
	f *descriptorpb.FieldDescriptorProto,
	scope string,
	siblings map[string]*descriptorpb.DescriptorProto,
) (*descriptorpb.DescriptorProto, error) {
	name, ok := localName(scope, f.GetTypeName())
	if !ok {
		return nil, malformedf("group %s names %q, which is not declared in %q",
			f.GetName(), f.GetTypeName(), scope)
	}
	body, ok := siblings[name]
	if !ok {
		return nil, malformedf("group %s has no backing message %q", f.GetName(), name)
	}
	return body, nil
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

// anchor names a point in the order a message body or file is emitted in:
// after field index, or — once every field has gone out — after extend block
// index. [anchorStart] precedes both.
type anchor struct {
	extend bool
	index  int
}

var anchorStart = anchor{index: -1}

// before reports whether a is emitted strictly before b.
func (a anchor) before(b anchor) bool {
	if a.extend != b.extend {
		return !a.extend
	}
	return a.index < b.index
}

func (a anchor) String() string {
	if a == anchorStart {
		return "the start of the declaration"
	}
	if a.extend {
		return fmt.Sprintf("extend block %d", a.index)
	}
	return fmt.Sprintf("field %d", a.index)
}

// fieldAnchors maps each message synthesized by a field — a map entry, a
// group body — to the field that produces it.
func fieldAnchors(
	scope string,
	fields []*descriptorpb.FieldDescriptorProto,
	nested map[string]*descriptorpb.DescriptorProto,
) (map[string]anchor, error) {
	out := make(map[string]anchor, len(fields))
	for i, f := range fields {
		if entry, ok := mapEntry(f, scope, nested); ok {
			out[entry.GetName()] = anchor{index: i}
			continue
		}
		if f.GetType() != descriptorpb.FieldDescriptorProto_TYPE_GROUP {
			continue
		}
		body, err := groupBody(f, scope, nested)
		if err != nil {
			return nil, err
		}
		out[body.GetName()] = anchor{index: i}
	}
	return out, nil
}

// groupBodyAnchors maps each message synthesized by a group extension to the
// `extend` block that declares it.
//
// A group is legal inside an `extend` block, and its body message lands in
// the enclosing scope's message list just as a group field's does. Leaving
// those out of the schedule made the body both scheduled as a message of its
// own and emitted inline by its block, so the recompiled descriptor grew a
// duplicate.
func groupBodyAnchors(scope string, blocks []extendBlock) (map[string]anchor, error) {
	out := make(map[string]anchor)
	for i, b := range blocks {
		for _, f := range b.fields {
			if f.GetType() != descriptorpb.FieldDescriptorProto_TYPE_GROUP {
				continue
			}
			name, ok := localName(scope, f.GetTypeName())
			if !ok {
				return nil, malformedf("group extension %s names %q, which is not declared in %q",
					f.GetName(), f.GetTypeName(), scope)
			}
			out[name] = anchor{extend: true, index: i}
		}
	}
	return out, nil
}

// scheduleDeclared decides where each explicitly declared message in list
// must be emitted so that recompiling reproduces list in its original order.
//
// The messages producedBy names are synthesized by a declaration, so they
// anchor the sequence; everything between two anchors was declared there in
// source. what names the list, for errors.
func scheduleDeclared(
	list []*descriptorpb.DescriptorProto,
	producedBy map[string]anchor,
	what string,
) (map[anchor][]*descriptorpb.DescriptorProto, error) {
	sched := make(map[anchor][]*descriptorpb.DescriptorProto)
	last := anchorStart
	for _, n := range list {
		if a, ok := producedBy[n.GetName()]; ok {
			if a.before(last) {
				// Fields are emitted before extend blocks, so an extend
				// block that came first in source cannot be put back there.
				return nil, unsupportedf(
					"%s has %s, synthesized by %s, after a message synthesized by %s",
					what, n.GetName(), a, last)
			}
			last = a
			continue
		}
		if n.GetOptions().GetMapEntry() {
			// A map-entry message no field claims cannot be written back:
			// there is no `map<K, V>` spelling that would recreate it.
			return nil, unsupportedf("orphan map entry message %s", n.GetName())
		}
		sched[last] = append(sched[last], n)
	}
	return sched, nil
}
