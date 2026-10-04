# Go corpus state, 2026-10-04 (Sprint 374 close)

This is the dated record of the Go 1.27.1 corpus at the close of Sprint 374.
It replaces `go-corpus-state-2026-09-24.md` as the current state. The keyed
table is `go-corpus-state-2026-10-04.tsv`: one row per failing root and mode
with its status and reason. Counts here come from that table and from the run
evidence; nothing is rounded.

## Run identity

| Item | Value |
|---|---|
| Full barrier candidate | sh `32b84b16f`, bashsharp `b53db13`, bashy `6fff487d`, coreutils `a1ec5c72`, yoke `8d456af0`, ycode `f3ceef62`, outpost `4f1b6ac3` |
| Harness | bashsharp-tests `7fb6807` |
| Toolchain | Go 1.27.1 linux/amd64, authenticated SDK |
| Host | 4-vCPU, 12 GB Linux amd64 cloud host, GOMAXPROCS=2 |
| Barrier evidence archive | `s374-final-evidence.tar.gz`, SHA-256 `871d9daf95f383dc1588fa04227f1434f1d436e6013bbd4455c24bd05c231045` |
| Head at sprint close | sh `3547757c3` (four commits after the barrier candidate) |

The full barrier ran on sh `32b84b16f`. It found two regressions, which were
fixed afterwards. The head `3547757c3` was verified by a targeted Linux run of
six roots (the two regressions, three roots fixed earlier in the sprint and the
`liveness` package: 6 of 6 in interpreted and compiled mode) and by both
engine test gates. It was not verified by a second full barrier; that run is a
task of the next sprint.

## Per-lane counts (full barrier)

| Lane | Native | Interpreted | Compiled |
|---|---|---|---|
| testdir (2,738 results, inventory 2,728) | 37 non-PASS | 316 non-PASS | 37 non-PASS |
| type checker (899 raw results) | 2 non-PASS | 2 non-PASS | 2 non-PASS |
| packages (26) | 0 non-PASS | 9 non-PASS | 0 non-PASS |

Failing rows after applying the harness rules: 288, all interpreted. Compiled
mode has none. The baseline run of this sprint (sh `90432168`) had 321.

## The type-checker lane

The lane runs the test packages `cmd/compile/internal/types2` and `go/types`.
It is scored on the inventoried roots only, which are per-file subtests such as
`TestFixedbugs/<file>`; 156 of them are declared native-only and earn no
credit. The 2 non-PASS in every lane are one skipped file in each package
(`TestFixedbugs/issue78346.go`), skipped by the native toolchain as well.

Four whole top-level tests in each package fail in both Bash# modes and pass
natively: `TestLongConstants`, `TestIndexRepresentability`, `TestIssue43124`,
`TestIssue59944`. They are not roots of the corpus inventory, so they are
outside the score. They were already failing at the sprint baseline and are
recorded here so they are not invisible.

## The interpreted package roots

All 26 package roots pass compiled. Nine fail interpreted. By operator
decision of 2026-10-04 a whole multi-package Go program is run compiled (it
enters a script by `embed go` or a `~~~go` fence), so these roots leave the
interpreted gate; the design review that confirms or reopens this is
`bashsharp-design-review-2026-10.md`.

| Package | Observed cause |
|---|---|
| `cmd/compile` | script tests rebuild the compiler under the interpreter; 10-minute limit |
| `cmd/compile/internal/importer` | about 375 subtests at 2.5 s each; 10-minute limit |
| `cmd/compile/internal/test` | one test alone takes 353 s; 10-minute limit |
| `cmd/internal/testdir` | 1,256 subtests pass with no failure, then the 10-minute limit |
| `cmd/compile/internal/types2` | killed for memory near 12 GB (collector pacing, transport allocation) |
| `go/types` | same |
| `cmd/compile/internal/syntax` | same, plus the volume of a whole-standard-library test |
| `cmd/compile/internal/ssa` | seven engine defects fixed in this sprint; of 9 tests run singly 4 passed, 2 hit a defect since fixed, 3 exceeded four minutes |
| `cmd/compile/internal/amd64` | 9 of 10 tests pass; `TestGoAMD64v1` inspects its own compiled binary |

## Regressions the barrier found

| Root | Cause | Resolution |
|---|---|---|
| `testdir:typeparam/ordered.go` | a NaN written back inside a slice by a dependency lost its carrier | fixed in sh `fb27dd4c0` with an end-to-end test |
| `testdir:fixedbugs/issue27695.go` | a reflected method value lost its receiver under constant collection; once that was fixed, a table of live origins ran a full collection on every bridged call | fixed in sh `073e77d30` and `fa4307a19`; 120 s to 3 s with two processors |

## Tour of Go and Go by Example (interpreted, same host class)

| Suite | Sprint baseline sh `90432168` | Head sh `3547757c3` |
|---|---|---|
| Tour of Go | 96 of 97 | 96 of 97 |
| Go by Example | 84 of 85 | 85 of 85 |

The Tour failure at both commits is `_content/tour/solutions/image.go` at the
unchanged 60 s deadline: the PNG encoder calls the program back 65,536 times.
Baseline and compiled modes are 97 of 97. It is the first story of the
performance sprint. The Go by Example gate reports 255 of 255 observations at
the head; at the baseline `examples/mutexes/mutexes.go` timed out.

## New exclusions are proposed, not yet in the pinned catalog

`go-corpus-exclusions.tsv` is a pinned projection of `go-corpus-targets.tsv`
with a validator (`tools/validate-go-corpus-catalog.sh`) that fixes its counts
and its family list. It is unchanged. The 21 exclusions decided in this sprint
(9 package roots and 12 single files) are in
`go-corpus-exclusions-2026-10-04-proposed.tsv`, in the same column layout, with
two families the catalog does not yet allow (`multi-package-interpreted`,
`assembly-input`). Folding them into the targets table, the validator and the
published denominator is part of the design-review decision.

## Decisions taken after this record (2026-10-04, design review)

The operator decided the six questions of `bashsharp-design-review-2026-10.md`.
For this table: 11 of the 16 rows carried for performance are pure
computation and become exclusions of a new family, `compute-bound` (listed in
the proposed table, now 32 rows); the other 5 expose engine overhead and stay
with the performance sprint together with the Tour program
`solutions/image.go`. The first authoritative recalculated ledger will come
from a full barrier on sh `3547757c3` or later.

## Ledger


Source run: final barrier, sh 32b84b16f. One row per failing root and mode in `go-corpus-state-2026-10-04.tsv`.

| Status | Count | Meaning |
|---|---|---|
| FIXED | 35 | failed on sh 90432168 (2026-10-04 baseline run), passes in this run |
| EXCLUDED | 249 | already in bashsharp/docs/go-corpus-exclusions.tsv |
| EXCLUDED-NEW | 21 | decided in Sprint 374 with the reason given; listed in `go-corpus-exclusions-2026-10-04-proposed.tsv` |
| CARRIED-376 | 16 | correct but too slow or too large; Sprint 376 |
| FIXED-AFTER-BARRIER | 2 | failed in this run, fixed afterwards on sh 3547757c3 and proved by a targeted run |

Failing in this run: 288. Compiled-mode failures: 0.

## Exclusions by family

| Family | Recorded | New |
|---|---|---|
| asmcheck | 74 | 1 |
| assembly-input | 0 | 4 |
| cgo | 8 | 0 |
| compiler-diagnostic | 104 | 4 |
| gc-observation | 15 | 1 |
| gc-only-check | 29 | 1 |
| multi-package-interpreted | 0 | 9 |
| unsafe-reinterpretation | 19 | 1 |

## New exclusions, each with its reason

| Root | Family | Reason |
|---|---|---|
| `package:cmd/compile` | multi-package-interpreted | whole-package Go source: compiled is the supported path (operator decision 2026-10-04, pending the Sprint 378 design review); observed: script tests rebuild the compiler under the interpreter |
| `package:cmd/compile/internal/amd64` | multi-package-interpreted | whole-package Go source: compiled is the supported path (operator decision 2026-10-04, pending the Sprint 378 design review); exclusion applies to ONE test, TestGoAMD64v1, which runs objdump on its own test binary to check the instruction set of generated machine code; under an interpreter there is no such binary. The package's other nine tests pass |
| `package:cmd/compile/internal/importer` | multi-package-interpreted | whole-package Go source: compiled is the supported path (operator decision 2026-10-04, pending the Sprint 378 design review); observed: sum of about 375 subtests at 2.5 s each |
| `package:cmd/compile/internal/ssa` | multi-package-interpreted | whole-package Go source: compiled is the supported path (operator decision 2026-10-04, pending the Sprint 378 design review); observed: seven engine fixes landed (slice headers, pool puts, call-result element address); of 9 tests run singly 4 passed, 2 hit the address bug since fixed, 3 exceeded four minutes |
| `package:cmd/compile/internal/syntax` | multi-package-interpreted | whole-package Go source: compiled is the supported path (operator decision 2026-10-04, pending the Sprint 378 design review); observed: collector pacing and allocation churn (story e6ead980); TestStdLib volume |
| `package:cmd/compile/internal/test` | multi-package-interpreted | whole-package Go source: compiled is the supported path (operator decision 2026-10-04, pending the Sprint 378 design review); observed: time: TestComparePanic alone takes 353 s on Linux |
| `package:cmd/compile/internal/types2` | multi-package-interpreted | whole-package Go source: compiled is the supported path (operator decision 2026-10-04, pending the Sprint 378 design review); observed: collector pacing and allocation churn (story e6ead980) |
| `package:cmd/internal/testdir` | multi-package-interpreted | whole-package Go source: compiled is the supported path (operator decision 2026-10-04, pending the Sprint 378 design review); observed: time: 1,256 subtests pass, 0 fail, then the 10-minute limit |
| `package:go/types` | multi-package-interpreted | whole-package Go source: compiled is the supported path (operator decision 2026-10-04, pending the Sprint 378 design review); observed: collector pacing and allocation churn (story e6ead980) |
| `testdir:asmhdr.go` | assembly-input | tests the gc toolchain itself, not Go language behaviour: a buildrundir test whose package includes a Go assembly (.s) file and the generated go_asm.h header |
| `testdir:closure3.go` | compiler-diagnostic | tests the gc toolchain itself, not Go language behaviour: errorcheckandrundir with -m: asserts the inliner's 'can inline' decisions |
| `testdir:codegen/switch.go` | asmcheck | tests the gc toolchain itself, not Go language behaviour: asmcheck: asserts the machine instructions gc emits for switch statements |
| `testdir:fixedbugs/issue20014.go` | gc-only-check | tests the gc toolchain itself, not Go language behaviour: runindir with -goexperiment fieldtrack and a linker -k flag: asserts the linker's field-tracking output |
| `testdir:fixedbugs/issue37513.go` | assembly-input | tests the gc toolchain itself, not Go language behaviour: a buildrundir test with an amd64 assembly source that raises SIGILL deliberately |
| `testdir:heapsampling.go` | gc-observation | asserts runtime.MemProfile samples attributed to the test's own function frames and source lines; interpreted allocations belong to interpreter frames, so the profile cannot be the program's without fabricating records |
| `testdir:linknameasm.go` | assembly-input | tests the gc toolchain itself, not Go language behaviour: a buildrundir test linking a Go declaration to an assembly symbol |
| `testdir:live_regabi.go` | compiler-diagnostic | tests the gc toolchain itself, not Go language behaviour: errorcheckwithauto with -live: asserts the compiler's liveness maps |
| `testdir:nilptr3.go` | compiler-diagnostic | tests the gc toolchain itself, not Go language behaviour: errorcheck with -d=nil: asserts which nil checks the optimizer removes |
| `testdir:prove.go` | compiler-diagnostic | tests the gc toolchain itself, not Go language behaviour: errorcheck with -d=ssa/prove/debug: asserts the prove pass's facts |
| `testdir:retjmp.go` | assembly-input | tests the gc toolchain itself, not Go language behaviour: a buildrundir test of the assembler's RET-to-symbol jump |
| `testdir:slice3.go` | unsafe-reinterpretation | a runoutput generator whose output reinterprets slice headers as *[3]uintptr to check cap arithmetic; the interpreter has no native memory layout for slice headers of interpreter-owned storage |

## Carried to Sprint 376

| Root | Family | Reason |
|---|---|---|
| `testdir:64bit.go` | performance | evaluation |
| `testdir:abi/fibish_closure.go` | performance | evaluation |
| `testdir:abi/uglyfib.go` | performance | evaluation |
| `testdir:copy.go` | performance | evaluation |
| `testdir:divmod.go` | performance | evaluation |
| `testdir:fixedbugs/issue13169.go` | performance | evaluation volume |
| `testdir:fixedbugs/issue20780b.go` | performance | evaluation, array copies |
| `testdir:fixedbugs/issue39541.go` | performance | callback round trips |
| `testdir:fixedbugs/issue59680.go` | performance | evaluation |
| `testdir:fixedbugs/issue78081.go` | performance | evaluation volume |
| `testdir:fixedbugs/issue79186.go` | performance | bridged sync and runtime calls |
| `testdir:fixedbugs/issue80188.go` | performance | memory proportional to live data (value representation) |
| `testdir:ken/chan.go` | performance | bridged sync calls |
| `testdir:ken/divconst.go` | performance | evaluation |
| `testdir:ken/modconst.go` | performance | evaluation |
| `testdir:stack.go` | performance | evaluation |

## Open

None.
