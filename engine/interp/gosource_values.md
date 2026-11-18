# Go-source call and result values (Sprint 118)

The frontend retains each expression in parallel short declarations and explicit
multiple-value returns. The interpreter evaluates them left to right before
binding destination cells. Existing declaration transactions preserve captured
cells when a short declaration reuses a name. The positioned fields survive the
AST walker, printer, typed JSON and native lowering.

Local function values retain interpreter closures. Imported function values carry
a checked signature and a native dependency handle; only the selected dependency
function executes in the bridge. Computed callees are evaluated once. This does
not send user function bodies to a Go compiler or bridge process.

Typed Go floating-point operands and results round at their declared precision.
Untyped constants remain exact until a typed boundary. This makes the unchanged
Tour loops solution agree with Go for the subtraction of its independently
computed square roots, while preserving huge constant cancellation and exact
fractional constant evaluation.

## Verification

`TestGoSourceCallValuesMatchGo` checks stdout, stderr and exit status against native
Go for three unchanged, SHA-pinned Tour files and five authored controls. It also
round-trips typed JSON, builds the lowered artifact, removes both source files,
and runs that artifact with an empty PATH. Controls cover mixed result types,
left-to-right result evaluation, computed-callee side effects, returned function
literals and captured-cell reuse. The originals are the Tour `moretypes/`
function-closures.go and function-values.go, and `solutions/loops.go`.

Focused complex-value, testing-session, exact constant and legacy projection
regressions also pass. The testing-session control deliberately contains a real
Go test skip; it is separate from these eight passing call cases.

This is a bounded call/result correction. It does not claim the entire Tour or
Go by Example interpreter corpus passes. Native callbacks accepting interpreted
functions, generic function instantiation, collection-contained function literals
and comprehensive nil/interface multi-result behavior require separate coverage.
