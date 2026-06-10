# Dual-compiler divergence notes

This file complements `sweep.txt` with what manual `TestDiffInspect`
runs have surfaced for individual fixtures. It is hand-maintained —
when a divergence is fixed (or accepted as a permanent difference),
update the entry here and re-run `TestSweep` to refresh `sweep.txt`.

## BOTH_OK_DIFFER

### desc_test_defaults.proto

Closed; the fixture now classifies as BOTH_OK_MATCH.

- **Float-default precision.** Resolved by routing `float` fields
  through 32-bit `strconv.FormatFloat` so single-precision defaults
  render at float32 resolution (`3.14159`) instead of the
  float32→float64 round-trip mantissa (`3.141590118408203`).
- **Enum-alias rendering on field defaults.** Resolved by having
  the experimental fdp generator pull the original identifier from
  `Value.ValueAST()` instead of `Member.Name()`. The default now
  renders with the alias the user wrote (`"ZED"`) instead of the
  primary enum-value name (`"ZERO"`).

### desc_test_complex.proto, desc_test_options.proto, desc_test_comments.proto

Closed; all three now classify as `BOTH_OK_MATCH`.

The root cause was that the experimental `fdp` generator serialises
every options message into a single `SetUnknown` blob at
`experimental/fdp/generator.go:options()`, so extensions defined in
the file being compiled (or its imports) came back as raw fields
after the new adapter's `proto.Marshal` / `proto.Unmarshal` round-
trip — plain `Unmarshal` has no resolver for file-local extensions.
Resolved by giving the new adapter a `dynamicpb.NewTypes` resolver
seeded with the file and its transitive imports, so the second
unmarshal pass restores typed extensions. The legacy adapter already
had this for free because it returns the proto from a linked
`linker.File` whose own extension types are registered.

### options/options.proto

Outstanding, but the divergence has shrunk to a single Any-value
field order:

```
"[bufbuild.protocompile.test.any]": protocmp.Message{
  "@type":    s"google.protobuf.Any",
  "type_url": string("type.googleapis.com/...AllTypes"),
  "value": []uint8{
-   0x82, 0x01, 0x03, 0x66, 0x6f, 0x6f, 0xca, 0x02, 0x04, 0x00, 0x01, 0x02, 0x03,
+   0xca, 0x02, 0x04, 0x00, 0x01, 0x02, 0x03, 0x82, 0x01, 0x03, 0x66, 0x6f, 0x6f,
  },
},
```

Same wire content, fields encoded in a different order inside the
Any's opaque `value` bytes (legacy emits field 16 then field 41;
experimental emits 41 then 16). Both are valid proto3 wire format;
neither matches a normative canonical order. Closing this fixture
either requires the experimental `ir`/`fdp` to emit fields in
source-declaration order (as the legacy seems to) or accepting this
as a permanent wire-canonical divergence inside Any values.

## NEW_FAIL

### options/test.proto, options/test_proto3.proto

Both fail with `cannot find message field 'bar' in
'google.protobuf.EnumOptions'`. The fixtures contain `option bar =
3.14159;` inside an enum body — `bar` is not a field on
`EnumOptions`. The legacy compiler silently drops the unknown
option (no `UninterpretedOption` entry either). The experimental
compiler errors strictly.

This is a behaviour philosophy gap, not a bug. The legacy lenience
is arguably wrong (silently dropping options loses user-supplied
information), but the test fixtures rely on it. Closing the gap
requires either making the experimental compiler lenient (matches
legacy behaviour, preserves test corpus) or trimming the malformed
options out of the fixtures (changes the test corpus, may break the
existing `TestOptionsEncoding` protoset goldens).

### options/test_editions.proto

Was: `mismatched types ... expected 'Foo' field, found 'Foo.Bar'`.

Closed at the symbol-resolution layer; fixture now classifies as
BOTH_OK_DIFFER. Cause was that protoc and the legacy interpreter
accept a delimited (group-encoded) field's element type name as a
synonym for the field name in text format (e.g. `Bar:` for a
`Bar bar = 2 [features.message_encoding = DELIMITED]` field).
The experimental `evalKey` only attempted the verbatim name and
then full symbol resolution, which finds the nested type `Foo.Bar`
and errors. Resolved by adding a lower-cased fallback that honours
the same conditions the legacy enforces (`options.go:2186-2206`):
type-name verbatim match, same scope as the field, delimited
encoding. The delimited check peeks at the field's AST options
because feature inheritance is computed later in lowering.

The remaining DIFFER is a separate bug in fdp marshalling:
DELIMITED fields are still emitted as length-prefixed wire format
(tag-`0x12`, len, body) instead of group format (start-`0x13`,
body, end-`0x14`). Same field-resolution path; different wire
encoding. Closing this needs `MessageValue.marshal` and the
extension-field marshalling to consult `isDelimited` and emit
SGROUP/EGROUP tags. That work is out of scope for the
symbol-resolution wedge.
