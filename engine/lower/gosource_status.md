# Ordinary Go goroutines do not carry shell command status

The previous proposed runtime mutex serialized only `shellrt`'s own accesses
inside Echo, Printf, Fail, and Exit. It did not protect the direct `rt.Status`
writes emitted before ordinary Go declarations, assignments, and updates.
An actual frontend/lowerer/native-artifact test with four independent local
counters and tuple assignments still reported multiple races after that mutex
patch; its native Go oracle passed. A direct runtime API test is not proof of
the emitted program's behavior.

GoSource emission now omits shell status resets and the shell status exit tail.
Go tuple assignments and compound updates use native Go operations. The latter
preserve Go's panic semantics instead of reporting a shell command failure and
continuing. A residual checked-operation failure in the GoSource path panics
rather than recording global command status. A resultless original Go function
cannot return a shell status. Deferred original panic uses Go's builtin rather
than the Classic shared panic-chain carrier.

Classic emission and the exported plain `shellrt.Status` API are unchanged.
This does not promise concurrent safety for Classic embedders that directly
share and mutate that variable, or for original Go programs with their own
races. No original Go program body is compiled into an interpreter dependency
helper; these tests exercise the normal public lowerer and native artifact mode.

`TestGoSourceStatus*` parses each original Go program, lowers its typed AST,
builds both original and generated programs with the real SDK and `-race`,
removes their source files, and runs both artifacts. It compares raw stdout and
stderr plus successful process status. The cases include separate mutable
locals, tuple results, concurrent recovered deferred panics, recovered operation
panics, and eight complete TCP ping/pong exchanges on an ephemeral port. They
also reject generated internal shell status references in these controls.
Classic tuple, printf-failure, numeric-update and status-clearing regressions
remain in the focused validation command. No port 8090 or corpus fixture is
modified.

Retained review logs under `.agents/review/` distinguish the rejected raw67
race (`status-raw67-race.log`) from corrected artifact gates. The runtime mutex
submission is not part of this replacement commit.

The final focused `-race` gate passed: lower 21.829 seconds and shellrt 2.684
seconds. Raw output is retained in `status-release-race.log`. The rejected
raw67 generated artifact exited 66 with race reports; its original oracle
completed successfully.
