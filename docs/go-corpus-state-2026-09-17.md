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

## Sprint 209 classification of the 631 failing roots

| class | roots | keys | meaning |
|---|---:|---:|---|
| **repair** | **284** | **304** | defect with an owner and first cause |
| **review** | 6 | 6 | unsafe policy decision pending |
| **blocked-design** | 76 | 83 | needs a design, not a batch |
| **excluded** | 265 | 272 | only the seven admissible compiler-artifact families |

Two regressions against Barrier C: `fixedbugs/bug285.go` interpreted (151),
`fixedbugs/issue43164.go` compiled (152). One residual key improved:
`fixedbugs/issue54220.go` interpreted now passes.

Owner cards: `todo:11f3abf68a13` (151) · `todo:c794caf2c0e2` (153) ·
`todo:600dd206218e` (152) · `todo:364913686dc6` (unsafe review) ·
`todo:ffabc6c1c44a` (finalizer) ·
`todo:27f3e89e5692` (blank.go unsafe) · `todo:cf82f587d115`
(`BASHY_HARD_IGNORE` leak into `--source=go` environments).

## What 100 % can honestly mean

The per-root catalog with class, family and reason is `docs/go-corpus-targets.md` / `.tsv` (repair **284 roots / 304 keys** · review 6 · blocked-design 76 · excluded 265 overall-classed roots). The exclusion list is `docs/go-corpus-exclusions.tsv`: **272 keys on 267 distinct root IDs**; two are mixed repair roots, hence 265 are overall-classed excluded. The non-excluded Sprint target is **366 roots / 393 keys**. Original denominator: **3,497 roots**. Sprint 209 carries it as a gate-required goal that cannot close with residue.

`bashpp-go-implementation-claim.md` D1 allows an adjusted denominator only when
it is published beside the original with every exclusion by root/mode ID and
reason. So the only defensible 100 % is:

> **every root passes every applicable mode**, where a mode is inapplicable to
> a root only if that mode would have to reproduce a compiler artifact
> (optimizer/asmcheck diagnostics, cgo, gc-only checks, native memory
> reinterpretation) — each such pair listed by ID in one published exclusion
> file, reviewed *downward* and never widened to make a number.

Under that definition the work is the **366-root / 393-key non-excluded
target**. Whole-corpus "3,497 / 3,497 including compiler diagnostics" is not
reachable by an interpreter and will not be claimed.

Constraints that stay: no timeout raises, no fixture edits, no native fallback
execution, no `GOSSAINTERP` bypass; repairs are bug fixes against the pinned
coordinate under the stable-package policy; the refactors (Sprints 208, 207)
gate on Barrier D′ ≡ Barrier D and precede the repair sprint unless the
operator reorders.
