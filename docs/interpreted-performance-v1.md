# v1.0 interpreted Go performance — evidence in progress

This release-note record separates completed correctness checks from performance
acceptance. It is not a declaration that all Go v1.0 gates have closed.

## Verified workload: fixedbugs/issue79186.go

The complete, unmodified Go 1.27.1 test passed in all three execution modes on
Linux, 4 vCPUs / 12 GB RAM, no swap. Its original assertions and expected output
were preserved. The interpreted backend identity and output were authenticated
by the existing harness verifier; outputs matched the native reference.

Frozen engine: `sh 0e1b001086de86b660154b3f165e6a4e85008f34`.
Language front end: `2389bc254680b7cb67d1911a9dc578b6b426acdf`.
Interpreter SHA-256: `1d2caf7618dd5af8a2ef983afbdfb7d874726692051b3668c849b5da48a57dab`.

| Mode | Correct completion | Wall seconds | GNU time maximum RSS (KiB) |
|---|---|---:|---:|
| Native reference | PASS | 13.212 | 170880 |
| Interpreted | PASS | 822.579 | 131208 |
| Compiled | PASS | 1.967 | 101760 |

Wall time includes harness and compilation overhead; the native run includes
initial cache-building costs. RSS is the command/descendant accounting reported
by GNU time, not aggregate concurrent memory or isolated interpreter RSS.
These numbers are not a controlled native-versus-compiled speed comparison.

The interpreted run took about 13 minutes 43 seconds. It used the harness's
existing diagnostic deadline override (1800 seconds per stage); the standard
60-second acceptance limit was not changed. Its standard-limit timeout remains
recorded. The operator authorized deferring proven performance limitations until
after v1.0, so this exact interpreted root is classified `compute-bound`.

## Remaining completion evidence

`64bit.go` passed the frozen candidate's ordinary interpreted gate in 34.13 s.
Full-workload interpreted completion checks for `divmod.go`, `ken/divconst.go`,
`ken/modconst.go`, `abi/uglyfib.go`, `abi/fibish_closure.go`, `copy.go`, `stack.go`,
`fixedbugs/issue13169.go`, `fixedbugs/issue59680.go`, and
`fixedbugs/issue78081.go` are still pending. Do not describe those ten workloads
as proven correct merely because they are historically excluded for performance.
Wrong results, mismatched panics, deadlocks and further timeouts require an
explicit unresolved disposition; they do not earn correctness credit.

## v1.0 workaround and follow-up

Move substantial computation into compiled Go, or an aliased Go fence called
from a mixed script. Keep the expensive loop and its working data inside the
compiled function to avoid repeated bridge calls. Existing bridge type and
ownership restrictions still apply; compilation is explicit, not a hidden
fallback used to pass interpreted tests. See [Go fences](fenced-go-and-shell-dialect-plan.md).

General interpreted loop/copy optimization is tracked after v1.0 by Sprint 380,
story `b47c0ba0d06d`: profile the verified workloads, evaluate typed loop/function
instructions, and assess native JIT only with measured benefit and preserved
semantics. No JIT capability is promised for v1.0 by this record.
