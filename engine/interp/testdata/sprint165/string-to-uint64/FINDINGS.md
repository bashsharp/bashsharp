# Sprint 165 string-to-uint64

Published sh `204b2974` still rejected a GoSource runtime scalar conversion with `BASHPP-EEXPR-CONVERT: cannot convert String to uint64` when the scalar value was stored in the shell string carrier but its Go type was numeric.

The outside-corpus reproducer in this directory uses non-constant `float32`, a named `float32`, a named `uint64`, and a named signed integer source. The test intentionally ignores the implementation-specific value of an out-of-range negative float-to-`uint64` conversion and asserts only that it runs like native Go.

The negative table pins the boundary: ordinary strings, malformed float carrier text, signed text for unsigned carriers, overflow for narrow source types, and missing source type metadata do not become permissive conversions.
