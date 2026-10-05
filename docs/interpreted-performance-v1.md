# v1.0 interpreted Go performance — evidence in progress

This release-note record separates completed correctness checks from performance
acceptance. **Eight of twelve workloads have verified full-scale interpreted
completion; four remain pending.** This is not a declaration that all Go v1.0
gates have closed.

## Verified full-workload completion

The complete, unmodified Go 1.27.1 workloads below passed in native,
interpreted and compiled modes on Linux, 4 vCPUs / 12 GB RAM, no swap. Original
assertions and expected outputs were preserved. Exact outputs matched the
native reference, and the existing backend verifier passed for both product
modes. No source-specific shortcut or native fallback earned interpreted credit.

Frozen engine: `sh 0e1b001086de86b660154b3f165e6a4e85008f34`.
Language front end: `2389bc254680b7cb67d1911a9dc578b6b426acdf`.
Interpreter SHA-256: `1d2caf7618dd5af8a2ef983afbdfb7d874726692051b3668c849b5da48a57dab`.

| Root | Native wall seconds | Interpreted wall seconds | Compiled wall seconds | Interpreted maximum RSS (KiB) |
|---|---:|---:|---:|---:|
| `64bit.go` | — | 30.334 | — | 320384 |
| `divmod.go` | 0.715 | 1677.493 | 0.816 | 97536 |
| `abi/fibish_closure.go` | 0.715 | 898.599 | 0.865 | 121728 |
| `abi/uglyfib.go` | 0.866 | 2232.902 | 1.066 | 120064 |
| `copy.go` | 0.515 | 244.226 | 0.765 | 123264 |
| `fixedbugs/issue13169.go` | 1.266 | 174.095 | 1.367 | 207564 |
| `fixedbugs/issue59680.go` | 0.615 | 391.293 | 0.816 | 420740 |
| `fixedbugs/issue79186.go` | 13.212 | 822.579 | 1.967 | 131208 |

All table rows are PASS in all three modes. Dashes omit timings from this
summary; they do not mean missing execution evidence. For `64bit.go`, both
generation and execution phases of the `runoutput` recipe were verified.
Its ordinary frozen-candidate interpreted gate already passed in **34.13 s**;
the supplemental 30.334 s measurement does not replace that gate result.

Wall time includes harness and compilation overhead; the `issue79186` native
run includes initial cache-building costs. RSS is GNU time's command/descendant
accounting, not aggregate concurrent memory or isolated interpreter RSS.
These numbers are not a controlled native-versus-compiled speed comparison.
For the earlier published runs, native/compiled maximum RSS was
170880/101760 KiB for `issue79186` and 99456/102016 KiB for `fibish_closure`.

## Diagnostic budgets and preserved failures

The standard **60-second acceptance limit is unchanged**. Supplemental
completion runs normally used the harness's existing **1800-second per-stage
diagnostic override**. Correct completion beyond the standard limit proves a
performance limitation for that workload; it does not turn its original
acceptance timeout into PASS. The operator authorized deferring proven
performance limitations until after v1.0; their exact-ID dispositions remain
visible in the corpus catalog.

`abi/uglyfib.go` first reached that diagnostic deadline without completing:
**1801.516 s**, maximum RSS **121728 KiB**. Its initial native/compiled runs
passed in 0.865/1.066 s. That timeout remains recorded. A separate **7200-second**
diagnostic retry, started **2026-10-05 21:18:52 UTC**, completed interpreted
execution correctly in **2232.902 s** (about 37 minutes 13 seconds), as shown
in the table. Source and tool identities were unchanged, exact outputs matched,
and both product backend verifiers passed. It is now proven correct for the
full tested workload, while remaining outside the ordinary performance limit.

## Remaining completion evidence

Full-workload interpreted completion is still **unproven** for:

- `ken/divconst.go`
- `ken/modconst.go`
- `stack.go`
- `fixedbugs/issue78081.go`

Do not describe these four workloads as proven correct merely because they
are historically excluded for performance. Wrong results, mismatched panics,
deadlocks and further timeouts require an explicit unresolved disposition;
they do not earn correctness credit.

The [October 5 full corpus record](go-corpus-state-2026-10-05.md) contains the
ordinary gate results and all exact-ID exclusions. Longer diagnostic results
remain separate from those acceptance timings.

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
