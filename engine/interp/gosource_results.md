# Go result storage and imported call assignments

Sprint 118, story 53 (`99bd1de0093b`).

Imported calls assigned to existing variables used a local-function-only path.
Go-source assignment now invokes the dependency bridge once, retains each typed
result cell, checks arity, and uses the existing tuple assignment validation and
commit path. Interface destinations retain their static interface type and the
actual returned dynamic value, including nil-interface identity. Discarded
results still evaluate their source call. Left-hand repeated names commit in
source order after the RHS has been evaluated.

Named function results previously started as empty strings regardless of their
declared type. They now use the same declaration and zero-value machinery as
ordinary Go-source variables. This gives struct fields and nested arrays real
storage before the body runs, preserves nil pointer/map/slice/interface values,
and lets deferred closures mutate named result storage before final copying.
Imported named zero values retain native representation. Original method and
function bodies remain interpreted; no original source bytes are modified.

The new hooks are limited to `bashPPTupleAssignCall` entry and the named-result
binding block in `bashPPInvoke`. They preserve the native-member defer changes
and the callback panic guards in the separate callback submission `7e0b0544`.
No public loader dependency or runtime mode boundary is added.

## Validation and remaining generator failure

Eight authored native/interpreter/compiled comparisons cover native writer
assignment; error-bearing native tuples; named native scalar identity; RHS
single evaluation and repeated destinations; imported named zero results; the
unchanged carry-method reproduction; deferred struct/array mutation and copying;
and named scalar/struct/array/pointer/map/slice/interface zero results. Compiled
artifacts run after source deletion with empty PATH. These controls and existing
numeric, complex, callable-result regressions pass under the race detector.

The complete unchanged official `test/64bit.go` generator is still FAIL in the
interpreter. It passes line 717's imported writer assignment and reaches line
512, where `a.Com()` calls a chained local method result:
`a.Uint64().Com().Int64()`. Computed local method-value resolution remains
unsupported and its interruption diagnostic is degraded during unwinding.
A standalone extraction and an interpreter trace (read-only Go build overlay)
are retained as failure evidence. The product files do not contain tracing.

The compiled full generator still produces byte-identical output to native Go.
Its retained complete generated child remains PASS in native Go, the interpreter,
and the compiled artifact. No generator or child cases are capped or removed.
Raw statuses, original hashes, full output, failing repro, and focused logs are
in manager evidence `result-binding-010`; full corpus convergence remains open.
