# Go simple initializers and runtime numeric boundaries

Sprint 118, story 53 (`99bd1de0093b`).

The original official `test/64bit.go` generator uses assignment in an if
initializer. `BashPPIf.Init` historically accepted only a short declaration.
The additive `InitStmt` command field preserves another Go simple statement,
its source positions and semicolon, while keeping the existing short-declaration
API unchanged. The interpreter executes it once inside the if/else scope before
the condition. Walking, printing, typed JSON, profile checking and lowering all
consume the new field. Syntax parsing outside unchanged Go-source ingestion
keeps its existing dialect rules.

Runtime numeric conversion used the constant representability check even for
values marked as runtime. Go-source integer conversion now truncates a finite
floating operand toward zero using its exact numerator and denominator, then
applies the target integer width and signedness. Integer conversions wrap and
truncate at that width. Compile-time constants still use the old exact
representability path and are checked by go/types before ingestion. Untyped
huge, fractional and complex expressions are not prematurely rounded.

A nonconstant shift is runtime even when only its shift count is a variable.
For typed integers, counts at or above the width produce zero (or negative-one
for a negative arithmetic right shift), without allocating an enormous exact
integer. Ordinary-width arithmetic retains the existing typed wrapping rules.
This does not claim complete IEEE nonfinite/out-of-range floating behavior or
repair unrelated runtime panic representations.

## Focused validation

Six authored programs compare native Go, interpreted source, and compiled
artifacts: integer casts, finite floating truncation, initializer ordering and
scope, an explicitly initialized carry accumulator, large runtime shifts and
signed minimum division, and exact constants. Source bytes are checked unchanged;
compiled artifacts run after source deletion with an empty PATH. Positioned
negative checks still reject uint16(4294967295), int(3.9), and typed-constant
uint8 overflow. A separate metadata test checks walking, printing, positions and
typed JSON. Existing Bash++ scalar/projection tests remain the legacy gate.

## Complete original generator attempt

The full original generator and its retained 1,340,126-byte generated child were
attempted without edits or test caps. The compiled generator succeeds and its
entire output exactly matches native Go (SHA-256
`e27493a0460d2ffbfcc43de268cdc756bb3d5a6bc1be8e329bb70bcf41b78552`).
The unchanged retained child succeeds in the interpreter, native Go, and the
compiled artifact, with empty streams and status zero.

The original generator interpreter remains FAIL at line 717,
`bout = bufio.NewWriter(os.Stdout)`: imported call assignment is incorrectly
routed to the local result-bearing function path. A separate exact carry-method
extraction also exposed missing zero initialization of a named struct result.
Those are reported as remaining runtime failures, not removed coverage.
The first child attempt was stopped by its precise owned PID after excessive
memory growth from a huge runtime shift; that failed attempt is retained, along
with the complete successful retry after the width fix.

Detailed original commands, statuses, raw output, hashes and regression logs are
retained in the manager evidence directory `numeric-runtime-009`.
