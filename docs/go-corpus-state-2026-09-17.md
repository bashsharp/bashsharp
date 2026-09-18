# Go corpus state — 2026-09-17 (after Barrier D, go1.27.1)

> **Canonical copy.** Regenerated at every full barrier run of `bashpp-tests`; the umbrella keeps a one-line pointer here. Numbers are never edited by hand.

The single page to read before touching any Go-corpus number. Source of every
figure: `bashpp-tests/docs/upstream-harness/barrier-d.md`,
the umbrella's `docs/sprint-206-reconciliation.json` and `docs/sprint-206-residual-ledger.tsv`.

## Coordinate and candidate

| | |
|---|---|
| Go | **go1.27.1** — corpus (3,400 `test/**/*.go`), oracle, authenticated SDK (linux/amd64 bin/go `30969f97…`), evaluator allow-list, stdlib import inventory (180 paths), `toolchain` directives |
| Candidate | `integ-206`: sh `a83e6a33` · bashy `88f7dbf` · coreutils `20205973` · readline `b958823` · filebrowser `cde11469` · harness `8efe846`; `bashy.real` `9b3d2506…` (reproduced byte-identically by `make build`) |
| Host | certification droplet; `/srv/sprint206` = go1.27.1 SDK (`LEAF_SDK=/srv/sprint206`), `/srv/sprint142` = the go1.27.0 anchor, untouched |

## The numbers (Barrier D, 20:37–22:19Z, exit 3, survivors 0)

| runner | PASS both modes | FAIL | SKIP | roots |
|---|---:|---:|---:|---:|
| testdir | 2,092 | 599 | 37 | 2,728 |
| typechecker | 735 | 6 | 2 | 743 (+156 native-only, zero credit) |
| package | 0 | 26 | 0 | 26 (25 pass compiled; interpreted by ID) |
| **total** | **2,827** | **631** | **39** | **3,497** (3,458 applicable) |

Per lane: interpreted testdir 596 FAIL, compiled testdir **30** FAIL, typechecker
6 raw non-PASS per lane, compiled packages 25/26. Barrier C (go1.27.0, four
sprints earlier) was 2,722 / 734 / 39 of 3,495. Independent `corpus-verify`:
the same 312 known native-only executions, no other violation. Bash 5.3 OFF
86/86, Go by Example 255/255, Tour 291/291 on the same revisions.

## What "PASS" means here

A root passes when its **native** run passes (the oracle) and **both** Bash++
modes — `interpreted` (`bashy --bashpp --source=go`) and `compiled`
(lower → gc build → run) — reproduce it. Native PASS alone earns nothing. The
39 SKIPs are upstream's own `shouldTest`/`skip` decisions, frozen by ID.

## The 631 failing roots, by what they are

| class | roots | keys | meaning |
|---|---:|---:|---|
| **Compiler-artifact rows** (`retained`) | 183 | 183 | interpreted mode asks for gc optimizer diagnostics (`-m` inlining, `-d=`), asmcheck codegen patterns (`linux/amd64/v1`), cgo or generate phases. **Every one of them passes compiled.** An interpreter cannot emit a compiler's diagnostics; these belong on the exclusion list for the interpreted mode only. |
| **By-ID decisions (D1–D10)** | 138 | 142 | D9 finalizer/MakeFunc 29 · D5 gc-only checks 29 · D7 `unsafe.Pointer` memory reinterpretation 26 · D1 compiler packages run interpreted 25 · D2/D10 deadline family 24 · D4 cgo 6 · D3b front end 2; plus 5 roots on ledger-recorded limitations (unsafe slice data, reflect-owned mutation, gc stack budget). |
| **Recorded-only subtotal** | **327** | 331 | fail only on the classes above |
| **Repair work** | **304** | 334 | 301 roots fail only on repairable keys, 3 mixed; **300 of the 334 keys are interpreted-mode** |

## The 304 repairable roots, by owner and first cause

| owner (D partition) | keys | dominant first causes |
|---|---:|---|
| 151 evaluator | 207 | `BASHPP-ECOLLECTION-ELEMENT` 16 · `EEXPR-OPERAND` 13 · `EEXPR-CONVERT` 13 · `EBUILTIN-TYPE` 9 · `EPOINTER-TARGET` 7 · `EEXPR-FORM` 7 · `EEXPR-UNDEFINED` 6 · `gosource: unsupported call target` 6 (incl. the new go1.27.1 root `fixedbugs/issue80976.go`, instantiated method type arguments) · `ECOLLECTION-BOUNDS` 5 · a long tail of 2–3-key codes (selector, compare, assert, update, assign, nil) |
| 153 runtime | 54 | output barrier (stray output where `.out` is absent) 7 · `panic: interface conversion` 7 · `panic: runtime error` 5 · dependency mutation of the interpreter 4 · `panic: FAIL` 4 · native slice writeback 2 · deadline 2 · misc |
| unclassified + 154 | 43 | typechecker diagnostic-wording rows (`check_test.go:288`) 6 · interpreter carrier capacity 2 · `amask` assertions 2 · parser diagnostics (parenthesized `go` expression, `expected at most 2 expressions`) 4 · singletons |
| 152 lowering | 29 | `could not import C` in compiled mode 10 (candidate for D4 by ID) · unsupported execute/generate phases 7 · unused-import diagnostics 4 · inlining/`nilptr3` diagnostics 4 · `LOWER-ETYPE` regression `fixedbugs/issue43164.go` 1 · misc |
| package | 1 | `cmd/compile/internal/importer` compiled: `cannot use importer.Default()` |
| unsafe policy review | 6 | ledger rows not covered by D7; decide by ID or repair |

Two regressions against Barrier C: `fixedbugs/bug285.go` interpreted (151),
`fixedbugs/issue43164.go` compiled (152). One residual key improved:
`fixedbugs/issue54220.go` interpreted now passes.

Owner cards: `todo:11f3abf68a13` (151) · `todo:c794caf2c0e2` (153) ·
`todo:600dd206218e` (152) · `todo:306d0db8d77b` (unclassified/154/package) ·
`todo:364913686dc6` (unsafe review) · `todo:ffabc6c1c44a` (finalizer) ·
`todo:27f3e89e5692` (blank.go unsafe) · `todo:cf82f587d115`
(`BASHY_HARD_IGNORE` leak into `--source=go` environments).

## What 100 % can honestly mean

The per-root catalog with class, family and reason is `docs/go-corpus-targets.md` / `.tsv` (repair 296 · review 6 · blocked-design 71 · excluded 258 roots); Sprint 209 carries it as a gate-required goal that cannot close with residue.

`bashpp-go-implementation-claim.md` D1 allows an adjusted denominator only when
it is published beside the original with every exclusion by root/mode ID and
reason. So the only defensible 100 % is:

> **every root passes every applicable mode**, where a mode is inapplicable to
> a root only if that mode would have to reproduce a compiler artifact
> (optimizer/asmcheck diagnostics, cgo, gc-only checks, native memory
> reinterpretation) — each such pair listed by ID in one published exclusion
> file, reviewed *downward* and never widened to make a number.

Under that definition the work is the **304 repairable roots** plus a
downward review of the 327 recorded roots (the deadline family and the six
unsafe rows are the first candidates to leave the list). Whole-corpus "3,497 /
3,497 including compiler diagnostics" is not reachable by an interpreter and
will not be claimed.

Constraints that stay: no timeout raises, no fixture edits, no native fallback
execution, no `GOSSAINTERP` bypass; repairs are bug fixes against the pinned
coordinate under the stable-package policy; the refactors (Sprints 208, 207)
gate on Barrier D′ ≡ Barrier D and precede the repair sprint unless the
operator reorders.
