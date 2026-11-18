# Sprint 153 Concurrency Findings

Story S153.3 / Story-ID `76bbbdd0ebd5`.

## Closed

| root | mechanism | status |
|---|---|---|
| `fixedbugs/issue8039.go` | Deferred value-builtin operands were re-evaluated during unwind. `defer copy(dst, <-ch)` must receive and retain the source slice at the `defer` statement, while the `copy` mutation still runs at unwind. | Closed by `cd263710` (`interp: capture deferred builtin operands`). Original corpus root exits 0 in a bounded current run. |
| outside-corpus `deferred_receive_copy.go` | Same as above, with `deferred_receive_supported.go` as the nearby positive control. | Added in `cd263710`; `TestSprint153DeferredReceiveCopy` asserts stdout/stderr/exit and goroutine drain. |
| outside-corpus `pipeline_select_cancel.go` | Interpreted channel pipeline covers send, receive, close, cancellation via `select`, and goroutine completion. | Added in `d6af27b4`; `TestSprint153PipelineSelectCancel` asserts exact output and goroutine drain. |
| outside-corpus `deadlock_negative.go` | Owner goroutine blocks on an interpreted local-type channel with no active interpreted tasks. | Closed by `d6af27b4`; reports `fatal error: all goroutines are asleep - deadlock!` with exit 2 instead of hanging to the test deadline. |

## Open / Other Lane

| root | mechanism | status |
|---|---|---|
| `closure.go` | Original goroutine/closure fan-out still exceeds a 15s bounded current run with no stdout/stderr. Scaled closure controls from Spike P do not reproduce it. | Open for a later closure/task-capture reduction. No product change landed here. |
| `fixedbugs/issue5963.go` | Original `runtime.Goexit` + deferred channel send + `os.Exit` sequence still exceeds a 15s bounded current run silently. The scaled controls under `deadline/fixedbugs_issue5963` now finish quickly, so the remaining original behavior needs a smaller current reproducer. | Open. Likely runtime.Goexit/defer unwinding, but not closed in this pass. |
| `chan/sieve2.go` | Prime sieve pipeline exits with `bash++: task failed: exit status 1` in a bounded current run. | Open channel pipeline failure; needs a reduced interpreted-task failure site. |
| `chan/powser2.go` | Current run fails at `powser2.go:133` with `BASHPP-ENIL-DEREF: dereference of nil pointer`. | Recorded for evaluator lane #143 unless a later reduction proves the nil pointer is produced by channel transport. |
| `deferfin.go` | Original finalizer root still exceeds a 15s bounded current run silently. The scaled controls now finish, so the spike's fast `task failed` symptom is stale for the current tree. | Open for bridge/finalizer callback lane #150; no runtime-concurrency fix attempted. |
| `fixedbugs/issue5493.go` | Original finalizer root still exceeds a 15s bounded current run silently. The scaled controls now finish. | Open for bridge/finalizer callback lane #150; no runtime-concurrency fix attempted. |

## Commands

- `go test -count=1 ./interp -run 'TestSprint153'`
- Rebuilt lane-local `bashy.real` from the downstream bashy checkout with this workspace in `.bashy-build.mod`.
- Bounded manual probes used shell-managed 15s kills around `./bashy.real --bashpp --source=go --go-file <root>`.
