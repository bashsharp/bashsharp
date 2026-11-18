# Launching a callee that has no original function body

Sprint: #243
Story: #674
Story-ID: 63073886bfce

`testdir:goprint.go` and `testdir:fixedbugs/issue25897b.go` failed at the
`go` statement with

    gosource: unsupported task capture: the launched callee does not resolve
    to an original function body; an original Go closure captures its free
    variables by reference and this launch cannot be given that meaning

The first launches the predeclared `println`; the second launches `f(c)` a
hundred times where `f` is a reflected method value of a local type obtained
through `reflect.Value.MethodByName`. Neither callee has a body the lexical
capture analysis (`gosource_task_capture.go`) could walk, and the analysis
refused rather than fall back to the classic deep-copy snapshot. The refusal
was right for a closure; it was the wrong question for these callees, whose
whole input is their operand list.

## Semantics

Go fixes the function value and every operand of a `go` statement in the
launching goroutine, then runs the call in the new one. `gosource_task_native.go`
gives exactly that meaning to the bodiless callee classes, tried in the order
`bashPPCall` dispatches a direct call so that a launch names the same
function the call would:

- a **nil function value** — operands evaluated, the fault raised in the task;
- a **dependency operation** (`fmt.Println(x)`, `wg.Done()`, `mu.Unlock()`,
  `f(c)` with `f` a native function value) — the bridge request is prepared
  in the parent as a direct call prepares it, a method receiver is bound to
  its method value at the launch, and the task issues the request on its
  own runner so any callback the dependency raises (issue25897b's `Foo`) is
  served by the task that made the call;
- a **computed dependency function value** (`fs[i]()`, `.(func(M))(v)`) —
  operands bound to the asserted signature and carried as value cells
  outside the snapshot, as an original function's are;
- `close(ch)` — the channel operand fixed at the launch through the rule
  `defer close(ch)` already uses (`goSourceCloseBuiltinOperand`);
- `panic(v)` — the value and its report text evaluated through the direct
  call's own rule (`bashPPPanicOperand`, extracted from `bashPPPredeclared`),
  raised in the task with the task's trace;
- `print`, `println`, `clear`, `copy`, `delete`, `recover` — print operands
  rendered to their final text at the launch (the deferred-print rule);
  every other operand retained as the value cell it produced, bound under a
  private name in a scope of the task's own, so the task sees the operand's
  type and `delete`/`clear`/`copy` address the parent's map or backing array
  as Go's reference semantics require.

Operands are never re-read in the task, not even against the snapshot: a
computed operand runs once, in order, in the parent, and a panic while
evaluating one launches nothing. The snapshot is the GoSource task scope
(non-nil capture set), so no unrelated parent local is copied; the carried
original callable descriptors keep their exact lexical references.

## Retained refusals

- A dependency call whose operands carry an original closure as a callback
  (`go sort.Slice(s, func(i, j int) bool {…})`): the closure was registered
  over the parent's live scope chain, and the task would run it there while
  the parent keeps executing — an interpreter data race, not merely a
  program one.
- `sync/atomic`, `unsafe.String` and runtime stack selectors, which the
  bridge answers over interpreter-owned storage: there is no launch-time
  operand value to carry.
- A value-producing builtin in statement context (`go len(s)`) is rejected by
  the front end as the compiler rejects it; the launch path refuses it too
  should it ever arrive by another route.

## Measurement

`gosource_task_native_test.go`: the two originals, verbatim
(`testdata/gosource-task-native/`, provenance recorded), in all three modes;
sixteen authored three-mode controls for each callee class, operand fixing,
evaluation order, operand panics, map/backing-array identity, shadowing and
a launch from a deferred function; interpreter-only controls for the panic
classes (task failure status and report text) and for each explicit refusal.

Computed callees are resolved once before classification. Original closures reuse
the existing pinned task-argument path; original method receivers remain owned
by that path. Panic typing reads a retained operand binding, so its separate
type inspections cannot repeat user code. Computed native callees retain the
same callback-closure refusal as named dependency calls.
