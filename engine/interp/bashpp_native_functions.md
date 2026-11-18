# Original Go function callbacks into dependencies

Sprint 118, story 54 (`c3a60493cde9`). This slice extends the existing native
value bridge with session-scoped handles for interpreted function declarations
and closures. The helper creates a typed `reflect.MakeFunc` trampoline. Arguments
and results cross the existing protocol; the original function body, captured
cells, and source positions remain in the interpreter. No original function body
is emitted into the dependency helper.

A callback runs on the Runner servicing its owning request, using the existing
callback admission gate. Nested synchronous dependency calls can reenter that
same request; competing tasks do not acquire another task's callback frame.
Handles carry a dependency-session identity and are rejected after Reset or when
presented to another session. Callback interpreter errors and cancellation
propagate to the request. Original panic transport preserves the interpreter's
existing string payload representation, including recovery by the original
caller and dependency `fmt` recovery; arbitrary typed panic payloads remain an
existing runtime limitation.

The initial policy permits synchronous rune predicate/mapping callbacks in
`strings` and `bytes` (`Map`, `FieldsFunc`, `ContainsFunc`, `IndexFunc`,
`LastIndexFunc`, `TrimFunc`, `TrimLeftFunc`, `TrimRightFunc`) and `sync.Once.Do`.
Only nonvariadic scalar parameter/result signatures are admitted. Imported
function values and method values can identify their callable using host-only
canonical import/type metadata; display type names do not authorize ambiguous
pointer receivers. Successful synchronous calls do not mark their returned
ordinary data as retaining callbacks.

This is a bounded capability, not complete general callback support. Unknown or
retained/asynchronous callback APIs fail before invocation. In particular:

- `sync.WaitGroup.Go` needs callback ownership and scheduling that outlive the
  submitting native request, with joined cancellation and session cleanup.
- HTTP handlers need registration lifetime and concurrent request ownership.
- `filepath.WalkDir` needs imported interface/error argument and result transport
  beyond this scalar signature slice.
- `testing.Main` and `FailNow` need scheduler-aware `runtime.Goexit` behavior;
  the separate hosted Go test driver is not evidence that these native callbacks
  work. They are not admitted here.

No fixture or corpus denominator changes are part of this implementation.

## Validation

`TestGoSourceNativeFunctionThreeModes` compares identical authored Go bytes in
the native oracle, interpreter, and compiled artifact. The artifact runs after
removing both sources, with an empty PATH. Eight cases cover captured mutation,
returned closures, named predicate values, nested callback reentry, concurrent
independent tasks, panic recovery, and Once state after a panic. Predicate results
are subsequently passed to another dependency call to detect false callback
retention. These are focused regressions, not upstream corpus passes.

`TestGoSourceFunctionCallbackCancelReset` cancels a callback during a nested
native sleep, then reuses the Runner successfully after Reset.
`TestGoSourceFunctionCallbackStaleSession` injects an actual prior-session handle
while a new session callback is active and checks rejection before dispatch.
`TestGoSourceFunctionCallbackLifetimeBoundary` verifies that native Go executes
an unchanged WaitGroup callback while this interpreter explicitly rejects it
before callback or following-statement effects.

The callback, original local-method transport, and callable-value regression
selection passed under the race detector. The two closure/Once additions and
stale-session test also passed separate focused race runs. Broader sprint and
classic-shell gates remain with the integration manager.
