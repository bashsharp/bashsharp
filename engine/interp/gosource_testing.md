# Interpreted Go test callbacks

Sprint: #118; Story: #56; Story-ID: 3ef468f4e831

`Runner.LoadGoSourceTests(ctx, program)` resets the runner and loads a non-main
package produced by `gosource.Load(sources, Options{RunMain: false, ...})`.
The caller supplies all applicable original companion files, authenticates their
bytes and records their source identities. Global initialization and original
`init` functions execute once through the interpreter. The session retains its
native dependency bridge until `Close` or `Runner.Reset`.

A dedicated native harness owns `testing.Main` and creates `testing.InternalTest`
callbacks whose entire generated body is:

```go
func(t *testing.T) {
    if err := session.Run(ctx, originalFunctionName, t); err != nil {
        t.Fatal(err)
    }
}
```

The original test package is never imported into that native harness. Its
functions are loaded ASTs, invoked by the interpreter. The real `*testing.T`
capability stays in the same process and on the native scheduler's callback
goroutine. Ordinary imported dependencies continue to use the existing bridge;
the dependency bridge must not compile or execute original test bodies.

The first adapter accepts original `func(*testing.T)` functions and implements
scalar `Error[f]`, `Log[f]`, `Fatal[f]`, `Skip[f]`, `Fail`, `FailNow`, and `SkipNow`
calls. Helper functions can receive the capability. Deferred receiver and
argument values are captured when the defer is registered. A private control
unwind runs every interpreted defer before invoking the real scheduler's
`FailNow`/`SkipNow`; it cannot be caught by interpreted `recover`.

A session and its runner are serial resources. Callbacks may reenter after the
previous callback returns or exits through the testing scheduler. Ordinary
`Runner.Run` cannot replace an active session; Reset invalidates old sessions.
Unsupported testing methods fail explicitly. `Run` and
`Cleanup` accept interpreted function literals; the real scheduler owns nested
callback goroutines and cleanup ordering. Run returns the scheduler
Boolean to interpreted expressions, including `if !t.Run(...)`. Test discovery
rejects generic functions and invalid `func(*testing.T)` signatures before
registration; TestMain requires its separate, still-pending testing.M driver. `Helper` accepts its no-argument form,
but native helper-frame attribution is not claimed. This is not a general
implementation of `testing.T`: Parallel, precise
Helper attribution, benchmarks,
examples, fuzzing, TestMain, composite formatting values and callbacks crossing
the native dependency-process boundary remain pending. Callers must turn every
returned interpreter error into a native test failure.

The pinned integration proof loads all four unmodified Go 1.27 `errors_test`
companions and invokes `TestNewEqual` and `TestErrorMethod`. Its source hashes are
checked before loading and bytes compared after execution. It is a two-function
slice, not all-stdlib product coverage. The corpus inventory and its full package,
source and dynamic-test denominators remain separate acceptance obligations.

Focused scheduler and lifecycle verification:

```sh
go test ./interp -run '^TestGoSourceTestingControlAndLifecycle$' -count=1
```

Pinned source integration (requires the authenticated Go 1.27 SDK as GOROOT and
toolchain, plus the frontend and dependency-bridge implementation):

```sh
go test ./interp -run '^TestGoSourceTestingOriginalErrors$' -count=1
```

The controls use separately authored adapter fixtures, fake capabilities for
precise log/defer assertions, and a child of the real test executable to verify
that an actual FailNow fails only its subtest while the following callback runs.
They also exercise actual SkipNow, cancellation, unsupported methods, initialization
and Reset invalidation. Passing them does not substitute for the pinned original
source integration proof.

The pinned four-companion integration now passes both selected original test
functions. Initial discovery failures exposed structured interface constraints,
unnamed receiver handling, imported type representation, native error identity,
and a dependency-process context that ended after package initialization. The
session now gives that process its own caller-supplied lifetime; callers must
keep the loading context alive until they finish the session. The original
source hashes and post-execution byte comparisons remain mandatory. This
establishes the two-function slice only; the remaining standard-library bodies
and unsupported testing methods above remain pending.

The complete `errors_test` corpus harness is separate from the two-function
regression. Product capabilities have no build tag. The explicit corpus tag
selects only the harness, which discovers every top-level Go Test declaration
from the original typed AST and invokes every discovered root:

```sh
go test -tags=gosource_testing_corpus ./interp -run '^TestGoSourceTestingErrorsCorpus$' -json -count=1
```

The first full replay discovers ten roots. Native Go passes those ten roots and
70 dynamic subtests; its twelve examples are recorded separately. The interpreter
passes TestNewEqual, TestErrorMethod and TestJoinReturnsNil, and actually fails
the other seven roots before their subtests start. All ten starts and terminals
are retained. Current failures concern interface/handle collection elements,
function literal elements, composite ranges, the predeclared any type, and indexed
non-scalar values. The full corpus gate consequently fails; missing dynamic
subtests are never credited as passed or skipped. The complete standard-library
obligation remains open.

## Second full corpus replay

The replay was repeated on the complete gate. Discovery again finds exactly ten
roots, and all ten are invoked and accounted for; none is skipped and no missing
dynamic subtest is credited. The native oracle for the same package passes ten
roots and 70 dynamic subtests. Those 70 belong only to TestIs (30), TestAs (18),
TestAsValidation (4) and TestAsType (18), so the three roots the interpreter
passes owe no subtests and take no unearned credit. The pinned source hashes
match before loading and the bytes are unchanged after execution.

The interpreter still passes TestNewEqual, TestErrorMethod and
TestJoinReturnsNil, and still fails the other seven with byte-identical
diagnostics, so the full corpus gate still fails. Every remaining root cause is
in the shared value model, ahead of any scheduler callback — each of the seven
fails before its first `t.Run`. Minimal repros, by original position:

| Root | Position | Diagnostic | Shape |
| --- | --- | --- | --- |
| TestJoin | join_test.go:28:19 | BASHPP-ECOLLECTION-ELEMENT: native handle (*errors.errorString) is not scalar | `multiErr{errors.New("err3")}` — composite literal of a named slice type whose element is a dependency handle |
| TestJoinErrorMethod | join_test.go:58:23 | BASHPP-ERANGE-TYPE / BASHPP-EEXPR-FORM: unsupported scalar expression *syntax.BashPPCompositeLit | `for _, test := range []struct{...}{...}` — range directly over a composite literal |
| TestIs | wrap_test.go:23:35 | BASHPP-ECOLLECTION-ELEMENT / BASHPP-EEXPR-FORM: *syntax.BashPPFuncLit | `&poser{"either 1 or 3", func(err error) bool {...}}` — function literal as a composite element |
| TestAs | wrap_test.go:101:30 | BASHPP-ECOLLECTION-ELEMENT / BASHPP-EEXPR-NIL: nil is not a scalar | `target any` field left `nil` in a table element |
| TestAsValidation | (registration-time) | undefined type: any | `testCases := []any{...}` — the predeclared `any` type |
| TestAsType | wrap_test.go:250:30 | BASHPP-ECOLLECTION-ELEMENT / BASHPP-EEXPR-NIL: nil is not a scalar | `nil` argument in a variadic aggregate |
| TestUnwrap | wrap_test.go:404:27 | BASHPP-EEXPR-OPERAND: indexed value is not a scalar | `tc.err` — selector on a struct range element holding an interface value |

None of these is owned by this adapter. They are reported as repros rather than
edited here, so the collection, native-handle, local-registry and lowering
owners each patch their own layer once.

## Guarded scheduler callbacks

Two measured defects on the callback path itself were repaired, using separately
authored fixtures rather than any original body.

`recover` reports "nothing to recover" through the exit status, because a panic
value may itself be the empty string. Go source form aborts a statement that
reports a non-zero status, so `defer func() { recover() }()` — the guard the
standard library writes when it only cares THAT a call panicked — terminated the
very frame it exists to let continue. That status is now marked errexit-exempt
where `recover` sets it, and the Go source statement rule honours the exemption
it already honours for errexit. Separately, a returned callback's residual
status no longer decides the test outcome: a Go `func(*testing.T)` returns no
value and has no exit status, so only a terminating condition — a fatal
interpreter error, an unrecovered panic, or an explicit exit — fails it.

The exemption is not a blanket one, and the callback reduction is keyed to
provenance rather than to the status value. Reading it as "status 1 does not
fail a callback" would reduce any native or runtime failure reporting 1 to a
pass. Instead `recover` stamps the status it reports with `Runner.bashPPRecoverSeq`
(recorded on `exitStatus.recoverSeq`), and `testingCallbackStatus` discards a
status 1 only when that stamp is the current one — recover reported it and
nothing has reported since — and only when the stamp was issued while that one
callback ran. A status carrying no stamp, a stamp superseded within the
callback, or a stamp left by an earlier callback is a real failure and is
returned unchanged, as is every terminating condition (explicit exit, fatal
exit, a panic still unwinding) and every status other than 1.

The provenance is what carries the guarantee, so it is pinned directly. These
cases hold the status at 1 and vary only the stamp; a blanket condition reduces
all of them to 0, so `unstamped status one`, `stamp superseded within the
callback` and `stamp issued before this callback` fail against a blanket form
and pass only against the narrowed one:

```sh
go test ./interp -run '^TestTestingCallbackStatus' -count=1
```

The end-to-end shapes are pinned separately: the guarded root, the guarded
`t.Run` callback, repeated empty subtest names, a helper function that receives
the capability and opens its own subtest, and — after a `recover` has already
stamped the runner — a deliberate unrecovered panic (`exit status 2`),
`t.Fatal` (fails via the capability) and a nil-channel send (`exit status 1`).
These pin the rule through the real scheduler but do not by themselves
distinguish the narrowed condition from a blanket one, because each of their
failures is already carried by `exit.err`, by the panic status, or by the
capability rather than by a bare residual status 1:

```sh
go test ./interp -run '^TestGoSourceTestingRecover' -count=1
```

This shape is exactly how the pinned corpus writes TestAsValidation's subtests,
so the repair removes a defect that would otherwise have failed four dynamic
subtests after the `any` type gap is closed. It does not by itself pass any
additional root; the corpus totals above are unchanged by it.

The further gap recorded here previously — inside a testing callback, a guard
that recovers an actual panic and binds its value
(`defer func(){ v := recover(); t.Log(v) }(); panic("deliberate")`) reporting
status 2 with no diagnostic and no log — no longer reproduces. It is now
pinned so it cannot regress unobserved, for a panic raised in the body itself
and for one raised in a called helper, both recovered and reported through the
capability:

```sh
go test ./interp -run '^TestGoSourceTestingRecoverBindsValue$' -count=1
```

One adjacent shape does still fail and is recorded rather than edited, because
it is not owned by this adapter: `recover` used directly as a call ARGUMENT,
`defer func(){ t.Log(recover()) }()`, reports
`BASHPP-EEXPR-UNDEFINED: undefined callable recover`. `recover` is answered as
a statement and as an assignment right-hand side, but not as a nested call
operand. The repair belongs to the predeclared-call lowering, not here.

## Cancellation of an in-flight nested callback

The lifecycle control cancelled only BEFORE a root callback started, where the
entry check in `Run` answers immediately and no body is abandoned. Cancelling
while a nested `t.Run` body is actually running was unpinned, and it dropped
obligations: cancellation abandons an interpreted frame the way a hard shell
`exit` does, and that path deliberately discards the deferred-call stack
instead of running it. The child body's own `defer` never ran, and neither did
the abandoned parent's.

`Runner.bashPPTestingCancelUnwind` now discharges an abandoned hosted-callback
frame's interpreted defers. They run detached from the cancelled context — each
call would otherwise abort on the same cancellation they exist to clean up
after — and with a cleared exit status, which is restored afterwards. This
finishes the obligations; it does not convert the outcome: `runFunction` still
returns `ctx.Err()`, the child callback still fails through the capability, and
the parent's remaining statements are NOT resumed. Cancellation abandons a
body; it does not let it keep running.

```sh
go test ./interp -run '^TestGoSourceTestingNestedCallbackCancellation$' -count=1
```

## Unexecuted standard-library obligations

This driver's executed set is the pinned discovery slice
`errors-original-errors-test-go-two-functions` — `TestNewEqual` and
`TestErrorMethod` from the unmodified `src/errors/errors_test.go`, loaded with
all four selected `errors_test` companions. Nothing else is executed, and
nothing else is discharged. The full denominator is retained as unexecuted,
never as successful and never as excluded:

| Obligation | Count | Status |
| --- | --- | --- |
| Reviewed packages | 180 | unexecuted, except the slice below |
| Host-exposed package roots | 178 | unexecuted |
| Policy refusals | 2 | refused, retained in the denominator |
| `errors_test` discovered roots | 10 | 3 pass, 7 fail with the repros above |
| `errors_test` dynamic subtests (native oracle) | 70 | unexecuted; owed by the 7 failing roots |
| `errors_test` examples | 12 | unsupported (example stdout routing unproven) |
| Slice actually claimed | 2 | `TestNewEqual`, `TestErrorMethod` |

Runtime test registrations and terminals are the denominator, not source-file
counts. `TestMain`, fuzzing, coverage/profiling, benchmarks and examples remain
separate phase obligations requiring the real `MainStart` contract; they are
recorded as pending, never omitted and never labelled successful. This slice is
not whole-corpus closure and no whole-corpus PASS is claimed.

The runtime accepts `GoSourceTestProgram`, a syntax-based view implemented by
`*gosource.Program`. Existing `LoadGoSourceTests(ctx, program)` calls are unchanged.
The view retains the original AST, package name, initializer order and SourceAt
mapping. This removes the production dependency from interp to the optional
Go-source frontend, so classic shell consumers do not link that frontend merely
because the runtime supports hosted tests. No build tags control this boundary.
