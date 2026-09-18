# CLAUDE.md — bashpp

Bash++ / Bash# — the dialect over the `sh` engine. This repo is **open
source**: never write hostnames, user paths, IPs, tokens, or proprietary
(cloudbox/kg/umbrella) details into any file, commit message, issue or PR.

## What lives here

- `docs/` — Bash++ design and status. Two kinds of page:
  - **Contracts and decisions** (`bashpp-posix-superset-syntax.md`,
    `bashpp-go-implementation-claim.md`, `bashpp-decorators-and-advice.md`,
    `bashsharp-ergonomics-tier.md`, `bashpp-import-resolution.md`, …). These
    are the source of truth for what the dialect admits; the collision map
    (Class R = stock bash rejects → purely additive; Class E = stock bash
    accepts → needs a table row + escape) governs every syntax addition.
  - **Go corpus status** (`go-corpus-state-<date>.md`,
    `go-corpus-targets.md` + `.tsv`). Regenerated at every full barrier run of
    `bashpp-tests` (`tools/upstream-harness/barrier-run.sh`). Never edit the
    numbers by hand; a new barrier writes a new dated state page and
    re-derives the target catalog. The four classes (repair · review ·
    blocked-design · excluded) and the **seven admissible exclusion reasons**
    (compiler-diagnostic, asmcheck, gc-only-check, unsafe-reinterpretation,
    gc-observation, cgo, assembly-companion) are defined there; a
    `blocked-design` family is a target with a prerequisite, never an
    exclusion.
- Go packages (Sprint 207 code move): `lower/`, `gosource/`,
  `syntax/typedjson/`. Until they land, the code is under `sh/`.

## Relationship to the siblings

| repo | role | this repo's dependency direction |
|---|---|---|
| `sh` | the engine: parser (`syntax`, carries the `BashPP*` nodes), `interp`, `expand` | bashpp **imports** sh; sh must never import bashpp (one exported evaluator hook, nil under `VSC_PROFILE=cert`, is the only seam) |
| `bashy` | the CLI that fronts both | imports both |
| `bashpp-tests` | the gate: Go 1.27.1 corpus, oracle, Tour, GbE, POSIX/Bash conformance | consumes the built `bashy` |
| `coreutils` | the POSIX-required ∪ GNU coreutils applets | unrelated to the dialect |

Flat-sibling layout: every cross-repo `replace` is `github.com/qiangli/<X> =>
../<X>` (`mvdan.cc/sh/v3 => ../sh`). Clone siblings next to each other.

## Change policy

The Bash 5.3 engine and the POSIX package are **stable — bug fixes only**;
feature-level change there happens only when the reference coordinate moves
(GNU Bash 5.3, Go 1.27.1). Dialect work lands **here**. A change that needs
an engine edit is a bug fix against the pinned coordinate or it is out of
scope.

## Gates

- `bashpp-tests/tools/classic-gate.sh` — Bash 5.3 OFF 86/86 and ON 79 + 7
  (the seven intentional raw reserved-`func` failures).
- `bashpp-tests/tools/upstream-harness/barrier-run.sh` — the full Go corpus
  replay (≈ 100 min on the certification host); a refactor is green only when
  its barrier equals the previous one **by ID**.
- A Bash++-ON change that is not Class R needs a collision-map row before code.
