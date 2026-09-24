# Go corpus state — 2026-09-24 (full run on `sprint-269`, go1.27.1)

> **Canonical copy; supersedes `go-corpus-state-2026-09-17.md`.** One full
> corpus run on the Sprint 269 reference tag. Every earlier class, owner card
> and story for the Go corpus is retired; the failures below were grouped
> from scratch from this run's own first lines. Numbers are never edited by
> hand. Per-root table: [`go-corpus-state-2026-09-24.tsv`](go-corpus-state-2026-09-24.tsv).

## Coordinate and candidate

| | |
|---|---|
| Go | **go1.27.1** — source corpus `go1.27.1.src.tar.gz` (sha256 `4e408aba…`, 3,400 `test/**/*.go`), authenticated SDK linux/amd64 `bin/go` `30969f97…` (the same binary as the 2026-09-17 run) |
| Candidate | tag **`sprint-269`**: sh `5d4c0e3d` · bashsharp `0d5aeb0` · bashy `8d8577e` · coreutils `cbf37428` · yoke `0bf2235` · readline `38ad08e` · filebrowser `c350ca3e` · harness (bashsharp-tests) `40137eb`; `bashsharp` sha256 `0b019d8c…` |
| Host | a fresh 2-vCPU / 4 GB Ubuntu 24.04 droplet, otherwise idle; `GOMAXPROCS=2 GOFLAGS=-p=2`, 60 s backend bound (unchanged) |
| Run | `barrier-run.sh` (every upstream root, native lane first), 2026-09-24 05:06–07:10Z, `corpus exit=3`, survivors 0; `corpus-verify` reports the same 312 native-only typechecker executions as every earlier barrier and no other violation |

## The numbers

A root passes when its **native** run passes (the oracle) and **both** Bash#
modes — interpreted and compiled (lower → gc build → run) — reproduce it.

| | PASS (both modes) | FAIL | SKIP | of applicable |
|---|---:|---:|---:|---|
| 2026-09-17 (Barrier D) | 2,827 | 631 | 39 | 82 % of 3,458 |
| **2026-09-24 (`sprint-269`)** | **3,078** | **380** | **39** | **89 % of 3,458** |

Per lane (`corpus-verify`, FAIL): interpreted testdir **351** (was 596),
compiled testdir **18** (30), typechecker **3** per lane (6), packages
interpreted **23** of 26 (26), packages compiled **9** of 26 (1). The 39
SKIPs are upstream's own frozen skips, unchanged.

Against 2026-09-17: **273 roots newly pass, 22 roots newly fail.** The 22
also fail with sh one commit before `sprint-269` (`f8212906`), so they
predate the last engine commit; they are flagged `new` in the table and lead
their groups.

## What the 380 failing roots are

**257 roots fail only on reviewed exclusions** (`go-corpus-exclusions.tsv`:
compiler diagnostics 103, asmcheck 74, gc-only checks 29, unsafe
reinterpretation 25, gc observation 18, cgo 8). They are not achievable by an
interpreter and are not work.

**123 roots are the Go work**, in six first-cause groups (each root in
exactly one):

| group | roots | new vs 09-17 | first cause |
|---|---:|---:|---|
| **G1 import resolution** | 33 | 9 | imported names missing after lowering (`LOWER-EUNDEFINED: undefined: fs / errors / obj / goarch / __gosource_import_…`), `cannot convert Int to fs.FileMode` and the compiled package roots |
| **G2 interpreter speed** | 34 | 8 | `command exceeded time limit` at the unchanged 60 s bound (many channel and typeparam roots) |
| **G3 native bridge / callbacks** | 15 | 0 | asynchronous or retained original callbacks, dependency mutation of interpreter-owned references, value-semantics callback parameters, mixed native/interpreted `select`, dependency-bridge build failures |
| **G4 language / type gaps** | 9 | 1 | map key types, `append` typing, assignment/const typing, selector roots, unsafe views |
| **G5 backend scope — review** | 12 | 0 | backend-unsupported inputs (generate/gotest phases, optimizer or codegen diagnostics); candidates for the exclusion list, decided by ID, never widened to make a number |
| **G6 runtime one-offs** | 20 | 4 | panics, output mismatches, redeclaration, goroutine stack limit |

## Exclusions to review

15 reviewed exclusion keys now **pass** and are flagged for removal from the
exclusion file (reviewed downward, never widened): cgo — `issue34968` (both
modes), `issue36705` compiled, `issue47227` compiled, `issue71225` and
`issue71226` (both modes); gc observation — `issue30476`, `gcstring`,
`mallocfin`, `tinyfin` (interpreted); assembly companion — `issue15609`,
`issue74648` (compiled); unsafe reinterpretation — `issue48536` interpreted.

## What 100 % can honestly mean

Unchanged: every root passes every applicable mode, where a mode is
inapplicable only when it would have to reproduce a compiler artifact — each
such pair listed by ID in the exclusion file. The non-excluded target is now
**123 roots**. Whole-corpus 3,497/3,497 including compiler diagnostics is not
reachable by an interpreter and will not be claimed.
