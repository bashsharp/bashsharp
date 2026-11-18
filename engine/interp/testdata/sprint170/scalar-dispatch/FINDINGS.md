# Sprint 170 scalar/type dispatch

`wide_uint.go` is an outside-corpus reproducer for a defined `uint64` that
crosses a native call. The shell carrier preserves its exact text, while the
bridge must resolve the scalar's underlying type before choosing its wire kind.
Treating it as a signed integer rejects values above `math.MaxInt64` before Go
can format or convert them.

The focused internal negative controls keep unknown type names unchanged and
reject signed text and out-of-range text for unsigned scalar destinations.
