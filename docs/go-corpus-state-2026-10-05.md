# Go corpus state, 2026-10-05 — frozen Sprint 376 candidate

This is the first full corpus record after Sprint 374, superseding the
[October 4 state](go-corpus-state-2026-10-04.md). The
[complete keyed table](go-corpus-state-2026-10-05.tsv) includes every observed
root/mode, with raw result, applicability and previous status. The
[comparison table](go-corpus-state-2026-10-05-compare.tsv) preserves exact IDs.

**Scored-root inventory reconciled. Separate full-workload correctness
checks now verify all 12 performance-limited workloads; see the
[measured performance limits](interpreted-performance-v1.md). Original
ordinary-gate failures below remain unchanged. This record does not declare
Go v1.0 complete.**

## Candidate and disposition

- Engine: `0e1b001086de86b660154b3f165e6a4e85008f34`.
- Tested language front end: `2389bc254680b7cb67d1911a9dc578b6b426acdf`.
- Bashy: `809a6c6f3fd8cda0b69408d4db84c25b0e6f32ef`.
- Full-run harness: `260dee6f8319b844855f78be12cb7ff7bc57d10a`.
- Interpreter SHA256: `1d2caf7618dd5af8a2ef983afbdfb7d874726692051b3668c849b5da48a57dab`.
- Go 1.27.1 Linux/amd64, 4 vCPUs, 12 GB RAM, no swap; `GOMAXPROCS=2`.
- Full raw archive SHA256: `ce1168fa2eac15c1121de99a409114351cc9e9b4dad590c495e3d1535fa9c1d8`.

The full run ended with exit **3**: **281 interpreted FAIL rows remain**,
all covered by the current exact-ID exclusion catalog. There are **zero new
scored-root failures, regressions or missing IDs**. The native and compiled
lanes each pass all **3458 native-applicable product roots**. Each mode also
has the same 39 expected skips, within the 3497-root product inventory.
The additional 156 native-only typechecker roots receive zero product credit.

The existing corpus observer confirms all 3653 observed IDs (3497 product plus
156 native-only) and the unchanged 39-ID skip set. Its nonzero result is
preserved: the 312 flagged native executions match exactly those 156 declared
native-only IDs in the two product modes, plus their aggregate diagnostic.
There are no unexpected observer violations. This is not a claim of zero
native executions across the unpartitioned observer input.

### Separate harness self-test correction

The original run also contains a stale cgo helper assertion failure in all
three modes, outside the scored root inventory. It expected blanket refusal
with cgo enabled, contradicting the authenticated cgo contract already added
in harness `5bd9787`/`649d05f`.

Harness **`9ec7b69`** corrects only that self-test and its authentication pin:
exact main-package source/companion identity is required when enabled, and
explicit refusal remains required when disabled. A separate Linux helper-only
replay passed all **28 terminal records in each mode**, without skips or failures.
The manager reviewed the unchanged runtime code and original failed evidence.
No upstream corpus assertion, workload, deadline or product code changed.
The raw failures shown below remain part of the full-run record; this supplement
resolves their stale-test cause.

### Required regressions and performance disclosure

`typeparam/ordered.go` and `fixedbugs/issue27695.go` pass interpreted and compiled.
The interpreted sentinels `rangegen.go`, `fixedbugs/issue9604b.go`, and
`fixedbugs/issue16249.go` pass in 50.65 s, 53.34 s and 51.59 s respectively;
all compiled counterparts pass too. Tour of Go **97/97** and Go by Example
**85/85** passed all three modes on this same product candidate.

The ordinary per-program acceptance limit remains **60 seconds**. Separate
longer-budget diagnostics establish whether the performance-excluded workloads
actually complete correctly; their progress and the compiled workaround are
in [v1.0 interpreted performance](interpreted-performance-v1.md). A catalog
exclusion alone does not prove completed correctness. `64bit.go` now passes
the ordinary gate in 34.13 s despite its historical exclusion row.

## Generated root accounting and original harness outcomes

Run completeness: **COMPLETE**. This file is produced by `script/sprint376/barrier-ledger.py`;
it is evidence for manager acceptance, not a closure claim.

Harness self-test failures outside the scored root inventory: **3**. Product exclusions do not settle these failures.
- `cmd/internal/testdir/TestResolveModuleProgramStillRefusesCgo` [native]: bashpp_backend_test.go:1708: resolveModuleProgram cgo error = <nil>, want existing non-Go refusal
- `cmd/internal/testdir/TestResolveModuleProgramStillRefusesCgo` [interpreted]: bashpp_backend_test.go:1708: resolveModuleProgram cgo error = <nil>, want existing non-Go refusal
- `cmd/internal/testdir/TestResolveModuleProgramStillRefusesCgo` [compiled]: bashpp_backend_test.go:1708: resolveModuleProgram cgo error = <nil>, want existing non-Go refusal

## Selected run identity (host redacted)

```
pin override (candidate m8-0e1b00108): shellrt_commit=0e1b001086de86b660154b3f165e6a4e85008f34 bashpp_version=bashsharp, Bash# (GNU Bash 5.3 superset), version 0.1.0-dev (2389bc2)
barrier=s376-final-m8 host=HOST harness=260dee6 bashpp=1d2caf7618dd5af8 sh=0e1b0010 candidate=m8-0e1b00108 start=2026-10-05T19:21:39Z
corpus exit=3 start=2026-10-05T19:21:39Z end=2026-10-05T21:22:42Z
survivors: 0
DONE s376-final-m8
```

## Results by lane and mode (from terminal events)

| Lane | Mode | PASS | FAIL | SKIP | NATIVE-ONLY | INCOMPLETE |
|---|---|---|---|---|---|---|
| testdir | native | 2691 | 0 | 37 | 0 | 0 |
| testdir | interpreted | 2419 | 272 | 37 | 0 | 0 |
| testdir | compiled | 2691 | 0 | 37 | 0 | 0 |
| typechecker | native | 741 | 0 | 2 | 156 | 0 |
| typechecker | interpreted | 741 | 0 | 2 | 156 | 0 |
| typechecker | compiled | 741 | 0 | 2 | 156 | 0 |
| package | native | 26 | 0 | 0 | 0 | 0 |
| package | interpreted | 17 | 9 | 0 | 0 | 0 |
| package | compiled | 26 | 0 | 0 | 0 | 0 |

## Aborted nested package tests

These nested tests started without individual terminals beneath an observed FAIL package terminal. The package root remains FAIL; no child result or passing credit is inferred.

| Lane evidence | Failed package | Aborted children |
|---|---|---:|
| evidence/evidence-interpreted/package | `cmd/compile/internal/importer` | 2 |
| evidence/evidence-interpreted/package | `cmd/compile/internal/ssa` | 1 |
| evidence/evidence-interpreted/package | `cmd/compile/internal/syntax` | 1 |
| evidence/evidence-interpreted/package | `cmd/compile/internal/test` | 3 |
| evidence/evidence-interpreted/package | `cmd/compile/internal/types2` | 1 |
| evidence/evidence-interpreted/package | `cmd/internal/testdir` | 952 |
| evidence/evidence-interpreted/package | `go/types` | 1 |

## Old (baseline) vs new, by ID

| Change | Count |
|---|---|
| NEW-FAILURE | 0 |
| REGRESSION | 0 |
| STILL-FAILING | 281 |
| DISAPPEARED-WITH-PASS | 5 |
| SKIP-OLD-ROW | 0 |
| SKIP-NOT-IN-BASELINE | 117 |
| NATIVE-ONLY | 468 |
| INCOMPLETE | 0 |
| MISSING | 0 |
| NOT-IN-RUN | 0 |

Failing rows not settled by the frozen catalog: **0** (UNEXCLUDED = no row anywhere; UNAPPROVED-PROPOSAL = proposed only, no operator approval; APPROVED-NOT-FOLDED = in --approved but not yet in the catalog, NOT settled; CARRIED-NOT-EXCLUDED = performance row, never an exclusion).
Extra non-inventory testdir terminals ignored: 0.

## New failures and regressions

None.

## Missing, incomplete or not in run

None.

## Old rows with an explicit PASS now

- `testdir:64bit.go` interpreted — DISAPPEARED-WITH-PASS (old CARRIED-376, now PASS; -) 
- `testdir:fixedbugs/issue20780b.go` interpreted — DISAPPEARED-WITH-PASS (old CARRIED-376, now PASS; -) 
- `testdir:fixedbugs/issue39541.go` interpreted — DISAPPEARED-WITH-PASS (old CARRIED-376, now PASS; -) 
- `testdir:fixedbugs/issue80188.go` interpreted — DISAPPEARED-WITH-PASS (old CARRIED-376, now PASS; -) 
- `testdir:ken/chan.go` interpreted — DISAPPEARED-WITH-PASS (old CARRIED-376, now PASS; -) 

## Failing rows needing an operator decision

None.
