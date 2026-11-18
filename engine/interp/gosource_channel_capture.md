# Typed channel operands in GoSource task capture

Sprint: #118; Story: #52; Story-ID: d564bada90bb

The unchanged timers example created timer2 in main and read timer2.C in an
original goroutine. Runtime channel evaluation used the positioned ChanExpr,
but lexical capture still examined its legacy Word. The flattened timer2.C
spelling is not a variable name, so the capture set omitted timer2. The task
then failed to resolve its structured receiver even after main printed the
expected Timer 1 fired and Timer 2 stopped lines.

The capture walker now prefers ChanExpr for sends and receives and ValueExpr
for send payloads. It walks typed selector roots and computed call operands
through the existing scope rules. Legacy words remain the fallback when typed
metadata is absent. Field names, literal text, shadowed locals, and declared
parameters do not become captures. This changes no native session, channel
protocol, snapshot storage, or Classic execution policy.

The existing unchanged timers fixture retains SHA-256
9a4f53271ce75bafb04decf8494af57b15923c17e28182beba4a7b23740a2989.
Five complete native/interpreted/compiled repetitions passed under race in
72.449 seconds. Four additional actual three-mode programs cover selector send
payloads, computed channels, native timer-field receives, and local shadowing.
Frontend capture precision, Classic exclusion, and stale/foreign capability
controls passed with those programs under race in 8.482 seconds.

A separate attempted control using Box{make(chan int,1)} remains blocked by
existing channel-valued struct element construction (scalar cannot be used as
chan int). Its exact authored source and failure log are retained as a separate
gap; it is not claimed fixed by this capture-only change. No original sources,
comparators, or deadlines were modified.

Evidence: $HOME/.local/state/bashy/sprint118-evidence/timer-lifecycle-028.
