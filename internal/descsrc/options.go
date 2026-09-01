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
	"sort"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

// normalizeOptions re-parses an options message through the wire format so
// its fields are typed.
//
// The compiler builds options generically from the IR and stores them as
// unknown fields — it has no need for Go types to emit a descriptor. Ranging
// over such a message sees nothing, so every option would look unnameable.
// Re-parsing decodes the standard fields, and resolves extensions that are
// linked into the calling binary, which is exactly the set whose names can
// be written back into source.
func normalizeOptions(opts proto.Message) (proto.Message, error) {
	raw, err := proto.Marshal(opts)
	if err != nil {
		return nil, fmt.Errorf("marshal options: %w", err)
	}
	fresh := opts.ProtoReflect().New().Interface()
	unmarshal := proto.UnmarshalOptions{Resolver: protoregistry.GlobalTypes}
	if err := unmarshal.Unmarshal(raw, fresh); err != nil {
		return nil, fmt.Errorf("re-parse options: %w", err)
	}
	return fresh, nil
}

// option is one rendered `name = value` pair, ready for either the statement
// form (`option name = value;`) or the bracket form (`[name = value]`).
type option struct {
	name  string
	value string
}

// collectOptions renders every set field of an options message.
//
// Standard options render by their field name; extensions render
// parenthesized by their full name, which is the source spelling for a
// custom option. Fields the descriptor could not interpret are refused:
// an unknown field carries only a number and bytes, so there is no name to
// write, and guessing would be exactly the silent corruption this package
// exists to avoid.
func collectOptions(opts proto.Message) ([]option, error) {
	if opts == nil {
		return nil, nil
	}
	if !opts.ProtoReflect().IsValid() {
		return nil, nil
	}

	normalized, err := normalizeOptions(opts)
	if err != nil {
		return nil, err
	}
	m := normalized.ProtoReflect()

	if unknown := m.GetUnknown(); len(unknown) > 0 {
		return nil, unsupportedf(
			"options carry %d bytes of unknown fields; the extensions defining them "+
				"are not linked in, so they cannot be named in source", len(unknown))
	}

	// uninterpreted_option only survives on a descriptor that was never
	// fully linked. Rendering the rest would silently drop it.
	if u, ok := normalized.(interface {
		GetUninterpretedOption() []*descriptorpb.UninterpretedOption
	}); ok && len(u.GetUninterpretedOption()) > 0 {
		return nil, unsupportedf("options carry uninterpreted_option; descriptor is not fully linked")
	}

	var out []option
	var rangeErr error
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		// map_entry is implied by the `map<K, V>` spelling and must not be
		// written back out; a message carrying it is suppressed entirely.
		if fd.FullName() == "google.protobuf.MessageOptions.map_entry" {
			return true
		}
		name := string(fd.Name())
		if fd.IsExtension() {
			name = "(" + string(fd.FullName()) + ")"
		}

		rendered, err := renderFieldValue(fd, v)
		if err != nil {
			rangeErr = err
			return false
		}
		for _, s := range rendered {
			out = append(out, option{name: name, value: s})
		}
		return true
	})
	if rangeErr != nil {
		return nil, rangeErr
	}

	// Range order is unspecified; sort so output is deterministic. Sorting by
	// name keeps repeated values of one option adjacent and in value order.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].name != out[j].name {
			return out[i].name < out[j].name
		}
		return false
	})
	return out, nil
}

// renderFieldValue renders one set field. A repeated field yields one string
// per element, since source repeats the option rather than listing it.
func renderFieldValue(fd protoreflect.FieldDescriptor, v protoreflect.Value) ([]string, error) {
	switch {
	case fd.IsMap():
		return nil, unsupportedf("map-valued option %s", fd.FullName())
	case fd.IsList():
		list := v.List()
		out := make([]string, 0, list.Len())
		for i := range list.Len() {
			s, err := renderScalar(fd, list.Get(i))
			if err != nil {
				return nil, err
			}
			out = append(out, s)
		}
		return out, nil
	default:
		s, err := renderScalar(fd, v)
		if err != nil {
			return nil, err
		}
		return []string{s}, nil
	}
}

func renderScalar(fd protoreflect.FieldDescriptor, v protoreflect.Value) (string, error) {
	switch fd.Kind() {
	case protoreflect.BoolKind:
		return strconvBool(v.Bool()), nil
	case protoreflect.EnumKind:
		ev := fd.Enum().Values().ByNumber(v.Enum())
		if ev == nil {
			// An unnamed enum value has no source spelling.
			return "", unsupportedf("option %s has unnamed enum value %d", fd.FullName(), v.Enum())
		}
		return string(ev.Name()), nil
	case protoreflect.StringKind:
		return quote(v.String()), nil
	case protoreflect.BytesKind:
		return quoteBytes(v.Bytes()), nil
	case protoreflect.MessageKind, protoreflect.GroupKind:
		return renderMessageValue(v.Message())
	default:
		return renderNumeric(fd, v)
	}
}

// renderMessageValue renders a message-valued option in the brace form
// protoc accepts for aggregate option values, e.g. `{ a: 1 b: "x" }`.
func renderMessageValue(m protoreflect.Message) (string, error) {
	if unknown := m.GetUnknown(); len(unknown) > 0 {
		return "", unsupportedf(
			"message-valued option %s carries unknown fields", m.Descriptor().FullName())
	}

	type entry struct {
		name string
		vals []string
	}
	var entries []entry
	var err error
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		name := string(fd.Name())
		if fd.IsExtension() {
			name = "[" + string(fd.FullName()) + "]"
		}
		var vals []string
		vals, err = renderFieldValue(fd, v)
		if err != nil {
			return false
		}
		entries = append(entries, entry{name: name, vals: vals})
		return true
	})
	if err != nil {
		return "", err
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].name < entries[j].name })

	var parts []string
	for _, e := range entries {
		for _, v := range e.vals {
			parts = append(parts, fmt.Sprintf("%s: %s", e.name, v))
		}
	}
	if len(parts) == 0 {
		return "{}", nil
	}
	return "{ " + strings.Join(parts, ", ") + " }", nil
}

// fieldOptions renders a field's bracket-form options, including the default
// value, or "" when there are none.
func fieldOptions(f *descriptorpb.FieldDescriptorProto) (string, error) {
	var opts []option

	// `default` lives on the field, not in FieldOptions, and must come from
	// the raw string with the field's own type deciding how to spell it.
	if f.DefaultValue != nil {
		v, err := renderDefault(f)
		if err != nil {
			return "", err
		}
		opts = append(opts, option{name: "default", value: v})
	}

	// json_name is always populated on a descriptor, but only an explicit
	// one is written back: re-deriving it from the field name is what the
	// compiler does, and a name that differs from the derived default was
	// therefore set in source and would otherwise be lost.
	//
	// Extensions are excluded because protoc rejects json_name on them, and
	// their descriptors carry the derived value regardless.
	if f.GetExtendee() == "" && f.GetJsonName() != "" && f.GetJsonName() != jsonName(f.GetName()) {
		opts = append(opts, option{name: "json_name", value: quote(f.GetJsonName())})
	}

	collected, err := collectOptions(f.GetOptions())
	if err != nil {
		return "", fmt.Errorf("field %s options: %w", f.GetName(), err)
	}
	opts = append(opts, collected...)
	return bracketOptions(opts), nil
}

func bracketOptions(opts []option) string {
	if len(opts) == 0 {
		return ""
	}
	parts := make([]string, 0, len(opts))
	for _, o := range opts {
		parts = append(parts, fmt.Sprintf("%s = %s", o.name, o.value))
	}
	return " [" + strings.Join(parts, ", ") + "]"
}

// renderDefault spells FieldDescriptorProto.default_value, which descriptors
// store as text regardless of the field's type.
func renderDefault(f *descriptorpb.FieldDescriptorProto) (string, error) {
	raw := f.GetDefaultValue()
	switch f.GetType() {
	case descriptorpb.FieldDescriptorProto_TYPE_STRING:
		return quote(raw), nil
	case descriptorpb.FieldDescriptorProto_TYPE_BYTES:
		// Stored with C-escaping already applied; re-quote the decoded bytes
		// so the escaping is this package's own and round-trips.
		b, err := unescapeBytes(raw)
		if err != nil {
			return "", fmt.Errorf("field %s default: %w", f.GetName(), err)
		}
		return quoteBytes(b), nil
	default:
		// Numeric, bool and enum defaults are already in source spelling.
		return raw, nil
	}
}

// jsonName derives the default JSON name for a field, matching protoc: an
// underscore is dropped and uppercases the character after it.
func jsonName(name string) string {
	var sb strings.Builder
	sb.Grow(len(name))
	upperNext := false
	for i := range len(name) {
		c := name[i]
		if c == '_' {
			upperNext = true
			continue
		}
		if upperNext {
			upperNext = false
			if c >= 'a' && c <= 'z' {
				c -= 'a' - 'A'
			}
		}
		sb.WriteByte(c)
	}
	return sb.String()
}
