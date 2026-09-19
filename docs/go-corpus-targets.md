# Go corpus targets — what a root is, which ones fail, and why the rest cannot pass

> **Canonical copy.** Regenerated at every full barrier run of `bashpp-tests`; the umbrella keeps a one-line pointer here. Numbers are never edited by hand.

Catalog: `docs/go-corpus-targets.tsv` — one row per failing root × mode at
Barrier D (665 rows, 631 roots), each with `class`, `family`, `reason`,
`destination`. This page defines the vocabulary and summarizes the catalog.
Numbers are from `integ-206` under go1.27.1 (`docs/go-corpus-state-2026-09-17.md`).

## Definitions

**Root** — one unit the upstream Go harness scores, named `<runner>:<id>`:

| runner | what a root is | count |
|---|---|---:|
| `testdir:<path>` | one file (or `.dir` directory) under `go/test/`, run by `cmd/internal/testdir` with the **action** in its first line — `run`, `rundir`, `runoutput`, `runindir`, `build`, `builddir`, `buildrun[dir]`, `compile[dir]`, `errorcheck*`, `asmcheck` (`none` = no header, matched by codegen rules) | 2,728 |
| `typechecker:<pkg>/<Test>/<file>` | one leaf of `go/types` and `cmd/compile/internal/types2` `check_test.go` (fixedbugs, spec, examples, …) | 743 (+156 native-only leaves, zero credit) |
| `package:<importpath>` | one compiler-internal package's own `go test` run as a unit | 26 |

**Mode** — every root runs three ways: **native** (the pinned Go toolchain; the
oracle, must pass or the root is SKIP/invalid), **interpreted**
(`bashy --bashpp --source=go`, the Bash++ interpreter), **compiled** (Bash++
lowering → gc build → run). A root **PASSes** only when both Bash++ modes
reproduce the native result. Bash++ modes are counted per (root, mode) pair =
a **key**.

**Applicable mode** — a Bash++ mode is inapplicable to a root only when passing
would require reproducing a *compiler artifact* (machine code, optimizer
diagnostics, gc-only checks, cgo, native memory or GC observation). Those pairs
are the exclusion list; nothing else is.

## The four classes

| class | roots | keys | meaning | what closes it |
|---|---:|---:|---|---|
| **repair** | **284** | 304 | a Bash++ defect with a first cause, owner, and mechanism (evaluator 151, runtime 153, lowering 152, one package) | code fix + reproducer + leaf; Barrier E |
| **review** | 6 | 6 | `unsafe.Pointer` rows not yet decided (repair or D7 by ID) | a decision on `todo:364913686dc6` |
| **blocked-design** | 76 | 83 | achievable in principle but needs a design, not a batch: multi-package interpreted execution (26), interpreter per-call cost / deadline (21), retained callbacks (12), source-preserving front end (5), harness generate phase (6), interpreter stack (5), reflect writeback (2), `blank.go` unsafe (1) | a design decision per family; until then FAIL by ID, never excluded |
| **excluded** | 265 | 272 | not achievable by an interpreter — see the table below | nothing; listed by ID in `docs/go-corpus-exclusions.tsv` with reason |

Precedence when a root has keys in several classes: repair > review >
blocked-design > excluded. The exclusion file has 272 keys on 267 distinct
root IDs; two also have repair keys, so 265 roots are overall-classed
`excluded`.

`284 + 6 + 76 + 265 = 631` failing roots; `2,827 + 631 + 39 = 3,497`.

## Why the 272 excluded keys cannot pass (seven families)

| family | roots | reason (the test asserts something only a compiler has) | evidence |
|---|---:|---|---|
| **compiler-diagnostic** | 103 | the root's expected output *is* gc's optimizer report: `-m` inlining/escape lines, `-d=` dumps, "can inline F", intrinsic substitution. An interpreter has no optimizer and no such report. Every one of these passes **compiled**. | `retained` rows; `errorcheck -m` files |
| **asmcheck** | 74 | codegen tests match regexes against the generated `linux/amd64` assembly; an interpreter emits no machine code. Pass compiled. | `test/codegen/*.go`, action `none` |
| **gc-only-check** | 29 | `errorcheck` expects a diagnostic only gc's front end produces; `go/types` (the checker Bash++ uses) cannot express it | D5 (162) |
| **unsafe-reinterpretation** | 26 | `unsafe.Pointer` casts that reinterpret raw memory layout (struct ↔ bytes, `unsafe.SliceData`, `abi/part_live*`): the interpreter has no native memory model to reinterpret | D7 (162), ledger `unsafe-slice-data` |
| **gc-observation** | 22 | `runtime.SetFinalizer` + `runtime.GC`, `testing.AllocsPerRun`, finalizer ordering, or `runtime.MemStats`: they observe the *native* garbage collector acting on interpreter-owned values, which it never sees | D7 note, D9 (165) |
| **cgo** | 16 | `import "C"` / `//go:build cgo`: the pure-Go product declares cgo out of scope | D4 (162) |
| **assembly-companion** | 2 | execute-phase `.s` companions: assembly must run natively | D3b (162) |

These are the **only** admissible exclusion reasons. Any new exclusion must
name one of them; a row that cannot is a repair or a blocked-design item.

## The blocked-design 76 — achievable, but each needs a decision first

| family | roots | what would have to exist | where the decision lives |
|---|---:|---|---|
| multi-package-interpreted | 26 | interpreting a whole module of compiler-internal packages (the 26 `package:` roots' interpreted mode; 25 already pass compiled) | 162 D1; `sh/docs/bashpp-multi-package-execution.md` |
| interpreter-per-call-cost | 21 | roots > 60 s interpreted (4.8×–53× the bound); the bound is never raised — the interpreter's per-call cost is the design item | 162 D2 / 165 D10; `sh/docs/bashpp-interpreter-per-call-cost.md` |
| retained-callback | 12 | `reflect.MakeFunc` / retained `reflect.ValueOf` / `SetFinalizer` original-pointer callbacks kept alive across native ↔ interpreter with correct lifetime | 165 D9; `todo:ffabc6c1c44a` |
| source-preserving-front-end | 5 | preserving gc parser trees and diagnostic positions where `go/parser` drops or rewrites the checked form | 165.5; `sh/docs/bashpp-source-preserving-front-end.md` |
| harness-generate-phase | 6 | `runoutput`/`errorcheckoutput` roots whose program *generates* the compile input; the interpreted backend does not drive that phase yet (harness, not language) | `retained` rows "generate phase" |
| interpreter-stack | 5 | deep recursion: one native stack frame per interpreted frame ("goroutine stack exceeds"), gc stack-stress budget | 165 D10e |
| reflect-writeback | 2 | reflection writeback into interpreter-owned values across the ownership boundary | claim D3 |
| unsafe (blank.go) | 1 | reinterpretation of an anonymous composite into named types | `todo:27f3e89e5692` |

## The repair 284 — by owner and first cause

| owner | keys | first causes (count) |
|---|---:|---|
| 151 evaluator | 219 | `BASHPP-ECOLLECTION-ELEMENT` 16 · `EEXPR-OPERAND` 13 · `EEXPR-CONVERT` 13 · `EBUILTIN-TYPE` 9 · `EPOINTER-TARGET` 7 · `EEXPR-FORM` 7 · `EEXPR-UNDEFINED` 6 · `gosource: unsupported call target` 6 (incl. `fixedbugs/issue80976.go`) · collection, generic, float and type-switch semantics |
| 153 runtime | 61 | output barrier 7 · `panic: interface conversion` 7 · `panic: runtime error` 5 · dependency mutation 4 · native bridge and zero-size carrier representation |
| 152 lowering | 23 | unsupported execute phases 4 · unused imports 4 · constant-group, init-task and parallel-assignment lowering · misc (`could not import C` compiled 10 reclassified to cgo exclusion) |
| package | 1 | `cmd/compile/internal/importer` compiled: `importer.Default()` type |

By action, the repair work is overwhelmingly **`run` (241 keys)**, then
`rundir` 23, `errorcheck` 8, `runindir` 8, `runoutput` 6 — i.e. programs that
should execute and print the expected output, not diagnostics.

## How the catalog is used

- `docs/go-corpus-exclusions.tsv` lists every excluded key by root/mode ID with
  reason and decision provenance. Original denominator: **3,497 roots**.
  Adjusted: **272 excluded keys on 267 IDs** (265 overall-classed excluded;
  two are mixed repair roots). The non-excluded Sprint target is **366 roots /
  393 keys** → 100 % means every other key passes.
- `blocked-design` rows are targets with a prerequisite, never exclusions;
  each family gets one decision, and a decision to *not* build it moves the
  family to `excluded` only if it can cite one of the seven reasons above —
  multi-package execution and per-call cost cannot, so they stay targets.
- Regenerate the catalog from a new barrier with
  `script/reconcile-sprint-206.py` (residual ledger) + the classification in
  this page; a root leaves the catalog only by passing at a full replay.
