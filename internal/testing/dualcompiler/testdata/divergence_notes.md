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
