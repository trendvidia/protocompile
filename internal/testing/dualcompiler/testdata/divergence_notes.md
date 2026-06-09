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

### desc_test_complex.proto, desc_test_options.proto, desc_test_comments.proto, options/options.proto

All four remaining `BOTH_OK_DIFFER` fixtures share a root cause: the
experimental `fdp` generator serialises every options message
(FileOptions, MessageOptions, etc.) into a single `SetUnknown` call
at `experimental/fdp/generator.go:options()`, instead of populating
the destination proto's typed fields. The wire bytes encode the same
semantic content, but the in-memory representation differs:

- The legacy adapter returns a `*FileDescriptorProto` with typed
  fields and typed extensions populated, because it pulls the proto
  from a linked `linker.File` whose own extension types are
  registered.
- The experimental adapter returns a `*FileDescriptorProto` whose
  `Options` blocks are one big lump of unknown wire bytes. After a
  `proto.Marshal` + `proto.Unmarshal` round-trip, standard fields
  like `go_package` come back typed because the global resolver
  knows about them, but extensions like `[testprotos.flfubar]` come
  back as raw fields because plain `Unmarshal` has no resolver for
  extensions declared in the file being compiled.

Three of the four fixtures (`desc_test_options.proto`,
`desc_test_comments.proto`, `options/options.proto`) have byte-
identical lengths between the two pipelines, but the bytes themselves
differ — strong evidence that the divergence is in field encoding
order, not content. `desc_test_complex.proto` has an 8-byte length
difference (6469 legacy vs 6461 experimental), suggesting one
extension option is serialised slightly differently.

`TestDiffInspect` prints both a structural `cmp.Diff` and
`byte-match=` / `proto-equal=` flags so the reader can distinguish
wire-byte order quirks from real semantic divergence. All four
fixtures show `proto-equal=false`, so each is a genuine divergence —
but closing them likely requires rewriting the fdp generator's
`options()` helper to populate typed fields and extensions instead
of relying on `SetUnknown`. That is its own architectural change,
not a small fix.

The naive workaround (skip the bytes round-trip in the new adapter
and return `fdp.DescriptorProto` directly) makes things worse,
because the in-memory proto still has every option field as unknown
bytes; even standard `go_package` (field 11 of FileOptions) comes
back as `"11": RawFields(...)`.

A trimmed `desc_test_complex.proto` inspect run (the
extension-range-options excerpt) shows the structural shape:

```
+ "20000":           protoreflect.RawFields{0x82, 0xe2, 0x09, 0x04, 0x6a, 0x61, 0x7a, 0x7a},
  "@type":           s"google.protobuf.ExtensionRangeOptions",
- "[foo.bar.label]": string("jazz"),
```

Same `"jazz"` payload either way; the only difference is whether
protocmp sees it as a typed extension or a raw field.

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

Different error: `mismatched types ... expected 'Foo' field, found
'Foo.Bar'`. Editions-specific type resolution variance. Not yet
inspected in detail.
