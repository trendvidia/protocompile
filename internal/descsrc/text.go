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
	"math"
	"strconv"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

func strconvBool(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// quote renders a Go string as a Protobuf string literal.
func quote(s string) string {
	return `"` + escape([]byte(s)) + `"`
}

// quoteBytes renders raw bytes as a Protobuf string literal. Protobuf has no
// distinct bytes literal; bytes values are written as escaped strings.
func quoteBytes(b []byte) string {
	return `"` + escape(b) + `"`
}

// escape C-escapes a byte string. Every byte outside printable ASCII is
// written as a hex escape, so the result is pure ASCII and never depends on
// the reader's encoding.
func escape(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		switch c {
		case '\n':
			sb.WriteString(`\n`)
		case '\r':
			sb.WriteString(`\r`)
		case '\t':
			sb.WriteString(`\t`)
		case '"':
			sb.WriteString(`\"`)
		case '\'':
			sb.WriteString(`\'`)
		case '\\':
			sb.WriteString(`\\`)
		default:
			if c < 0x20 || c >= 0x7f {
				fmt.Fprintf(&sb, `\x%02x`, c)
				continue
			}
			sb.WriteByte(c)
		}
	}
	return sb.String()
}

// unescapeBytes decodes the C-escaped form protoc stores in
// FieldDescriptorProto.default_value for bytes fields.
func unescapeBytes(s string) ([]byte, error) {
	var out []byte
	for i := 0; i < len(s); {
		c := s[i]
		if c != '\\' {
			out = append(out, c)
			i++
			continue
		}
		i++
		if i >= len(s) {
			return nil, fmt.Errorf("trailing backslash in %q", s)
		}
		switch e := s[i]; e {
		case 'n':
			out, i = append(out, '\n'), i+1
		case 'r':
			out, i = append(out, '\r'), i+1
		case 't':
			out, i = append(out, '\t'), i+1
		case 'a':
			out, i = append(out, '\a'), i+1
		case 'b':
			out, i = append(out, '\b'), i+1
		case 'f':
			out, i = append(out, '\f'), i+1
		case 'v':
			out, i = append(out, '\v'), i+1
		case '\\', '\'', '"', '?':
			out, i = append(out, e), i+1
		case 'x', 'X':
			i++
			start := i
			for i < len(s) && i-start < 2 && isHex(s[i]) {
				i++
			}
			if i == start {
				return nil, fmt.Errorf("empty hex escape in %q", s)
			}
			n, err := strconv.ParseUint(s[start:i], 16, 8)
			if err != nil {
				return nil, fmt.Errorf("bad hex escape in %q: %w", s, err)
			}
			out = append(out, byte(n))
		default:
			if e >= '0' && e <= '7' {
				start := i
				for i < len(s) && i-start < 3 && s[i] >= '0' && s[i] <= '7' {
					i++
				}
				n, err := strconv.ParseUint(s[start:i], 8, 8)
				if err != nil {
					return nil, fmt.Errorf("bad octal escape in %q: %w", s, err)
				}
				out = append(out, byte(n))
				continue
			}
			return nil, fmt.Errorf("unknown escape %q in %q", e, s)
		}
	}
	return out, nil
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

// renderNumeric spells an integer or floating-point option value.
func renderNumeric(fd protoreflect.FieldDescriptor, v protoreflect.Value) (string, error) {
	switch fd.Kind() {
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return strconv.FormatInt(v.Int(), 10), nil
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind,
		protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return strconv.FormatUint(v.Uint(), 10), nil
	case protoreflect.FloatKind:
		return renderFloat(v.Float(), 32)
	case protoreflect.DoubleKind:
		return renderFloat(v.Float(), 64)
	default:
		return "", unsupportedf("option %s has unhandled kind %v", fd.FullName(), fd.Kind())
	}
}

// renderFloat spells a float so that re-parsing yields the same value.
func renderFloat(f float64, bits int) (string, error) {
	switch {
	case math.IsNaN(f):
		return "nan", nil
	case math.IsInf(f, 1):
		return "inf", nil
	case math.IsInf(f, -1):
		return "-inf", nil
	}
	// 'g' with -1 precision is the shortest form that round-trips exactly.
	s := strconv.FormatFloat(f, 'g', -1, bits)
	// A value that reads as an integer must still be spelled as a float, or
	// it re-parses as an integer literal and changes the option's type.
	if !strings.ContainsAny(s, ".eEni") {
		s += ".0"
	}
	return s, nil
}

// editionName maps an Edition enum to the string used in an `edition = ...`
// declaration.
func editionName(e descriptorpb.Edition) string {
	// The enum names are EDITION_2023, EDITION_2024, ... and the source
	// spelling is the bare year.
	return strings.TrimPrefix(e.String(), "EDITION_")
}
