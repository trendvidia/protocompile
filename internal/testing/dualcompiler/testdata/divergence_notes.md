# Dual-compiler divergence notes

This file complements `sweep.txt` with what manual `TestDiffInspect`
runs have surfaced for individual fixtures. It is hand-maintained —
when a divergence is fixed (or accepted as a permanent difference),
update the entry here and re-run `TestSweep` to refresh `sweep.txt`.

## BOTH_OK_DIFFER

### desc_test_defaults.proto

Three classes of divergence:

1. **Float-default precision.** The legacy compiler renders single-
   precision floats with the textual form the user wrote: `3.14159`.
   The experimental compiler renders them with the full IEEE-754
   round-trip precision: `3.141590118408203`. Same for
   `6.022141e+23` vs `6.022141003837819e+23`. Both forms parse back
   to the same `float32`, but only the legacy form matches what
   `protoc` emits in `default_value`. The experimental pipeline
   should preserve the original token text on field defaults, or
   produce the round-down 7-digit form that matches the source.

2. **Enum alias rendering on field defaults.** When a field's default
   is given as an enum alias (e.g. `[default = ZED]` where `ZED` is
   an alias for `ZERO`), the legacy compiler preserves the alias
   name in `default_value` (`"ZED"`). The experimental pipeline
   resolves to the primary enum-value name (`"ZERO"`). The legacy
   behaviour matches `protoc`; the experimental behaviour loses
   user-supplied information.

3. **protocmp `@type` discriminator.** Not a real divergence; it
   appears in the `cmp.Diff` output because protocmp uses it as a
   message-type tag.

### desc_test_complex.proto

Larger diff (20-/43+ lines), first field is `enum_type`. Not yet
inspected in detail.

### desc_test_options.proto

Small diff (1-/1+ lines), first field is `message_type`. Not yet
inspected.

### desc_test_comments.proto

18-/21+ line diff, first field is `@type`. Likely related to either
the float-default or enum-default divergences above; not confirmed.

### options/options.proto

3-/12+ line diff, first field is `message_type`. Not yet inspected.

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
