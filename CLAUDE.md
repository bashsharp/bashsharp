# CLAUDE.md — bashsharp

**Bash#** (`bashsharp`; formerly Bash++ — `docs/naming-collision.md`: rail5/bashpp owns that name) — the language over the `sh` engine: GNU Bash 5.3 superset ·
POSIX.1-2016 · Go 1.27.1 mixed · fenced python/typescript/rust/c/c++ ·
`agentic` reserved for agentic features (the five clauses in README.md; the
plan of record for the code move is the umbrella's
`docs/sprint-211-master-execution-plan.md`). This repo is **open
source**: never write hostnames, user paths, IPs, tokens, or proprietary
(cloudbox/kg/umbrella) details into any file, commit message, issue or PR.

## What lives here

- `docs/` — Bash# design and status. Two kinds of page:
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
- Go packages (Sprint 211): the language's **front door**, moved here from
  bashy so the language is usable and testable without it.
  - `front/` — the dialect selector (`--bashsharp`/`--no-bashsharp`, `BASHY_BASHSHARP`, `.bsh`, binary default —
    the Bash#-era spellings are deprecated aliases reported on `Resolution.Deprecated`; `ResolveBashPP`, `LangVariant`,
    `ParserOptions`) and the direct Go-source interface (`--source=go`,
    `--go-package`, `--go-import-path`, `--check`, `--go-list`;
    `StripGoSourceInvocationFlags`, `ResolveGoSource`, `CollectGoSources`,
    `LoadGoSource`). `GoSourceLoad` / `GoSourcePackageFiles` /
    `GoSourceModuleDir` are package variables a binary wires — nil means "no
    Go front end in this build" and is a refusal, never a shell fallback.
    `Prog` is the name refusals are prefixed with.
  - `transpile/` — wires `sh/gosource` + `sh/lower` onto those hooks at
    init, and owns `transpile` (`Main(args) int`: `-o`, `--map`, library
    mode, `--go-native-unit`). Importing it is what turns the front end on.
  - `cmd/bashsharp/` — the binary: runs a `.bsh` (`.bpp` deprecated) / `-c` / stdin / `--source=go`
    program, `--check`, `--go-list`, `transpile`, `--version`, over the sh
    engine alone (stdio, PATH toolchains, no AgentOS). It takes the argv the
    corpus harness gives bashy, so `BASHPP_TOOL` can name it.
  - `front/import_graph_test.go` is the D1 ratchet: this module imports the
    sh engine and nothing from bashy, coreutils or yoke. It builds and tests
    in a checkout holding only `bashsharp` and `../sh`.
  - What is NOT here: the evaluator, `lower`, `gosource`, `polyglot` and the
    `LangBashPP` grammar — the next section and `docs/seam.md` say why.

## What stays in sh and why (Sprint 207 measured 2026-09-18; Sprint 211 re-measured the seam — `docs/seam.md`)

The 207 card allowed a code move only as "import rewrites" behind at most
one small exported hook, and said to stop and record otherwise. Measured:

- `syntax/typedjson` is **upstream `mvdan.cc/sh`** (the AST JSON codec that
  `shfmt --to-json` uses), not dialect code. It stays.
- `polyglot` is a leaf, but the **classic** engine imports it
  (`interp/api.go`, `interp/shell_polyglot.go`). Moving it would make the
  engine import the dialect repo — the inverse of the goal. It stays.
- `gosource` (the Go-source front end) is what **91 evaluator tests in
  `sh/interp` drive** — 24 of them `package interp` (they reach Runner
  internals and cannot leave), 67 `package interp_test`. Moving `gosource`
  out gives `sh` a test-only dependency on this repo: a module cycle
  (`bashpp` requires `sh`, `sh` requires `bashpp`), which is the dependency
  direction this split exists to forbid. `lower` is imported by `gosource`,
  so it goes only where `gosource` goes. Both stay.
- The evaluator itself — 140 `interp/bashpp_*.go` + `gosource_*.go` files —
  references **49 unexported classic `Runner` members** (`r.cmd`, `r.stmts`,
  `r.lookupVar`, `r.fdTable`, `r.exit`, `r.call`, …) plus ~660 of its own
  `r.bashPP*` methods. There is no small hook; the `bashPPEvaluator`
  interface at `interp/bashpp_import.go:62` is the seam and it is unexported
  by design. They stay.

So the isolation in force is the one that already ships: under
`VSC_PROFILE=cert` the evaluator is nil and the parser dialect is inert
(`bashpp-tests/tools/classic-gate.sh` proves OFF 86/86, ON 79 + 7), and the
Bash# evaluator TESTS sit behind the `full` build tag (`go test -tags full`).
The repo boundary that DID land is the one that costs nothing:
coreutils/yoke (Sprint 208). What would unlock a code move is a design
change, not a mechanical one — an exported evaluator seam plus an
evaluator test harness that does not live in `package interp`. Sprint 211
measured that seam (`docs/seam.md`: 204 distinct identifiers, 636
references, 71 `Runner` fields, 73 methods) and confirmed it is an engine
redesign, tracked as `todo:af65f24e` in the umbrella. Until it lands,
**Bash# engine changes still land in `sh/interp`, `sh/lower`,
`sh/gosource`**; the front door, the binary and the docs live here.

## Relationship to the siblings

| repo | role | this repo's dependency direction |
|---|---|---|
| `sh` | the engine: parser (`syntax`, carries the `BashPP*` nodes), `interp` (incl. the Bash# evaluator), `lower`, `gosource`, `expand` | bashpp **imports** sh; sh must never import bashpp — see §What stays in sh and why |
| `bashy` | the CLI that fronts both (`--bashsharp`, `transpile`, `bashy agentic`) | imports `bashsharp/front` + `bashsharp/transpile` (`replace ../bashsharp`) and sh |
| `bashsharp-tests` | the gate: Go 1.27.1 corpus, oracle, Tour, GbE, POSIX/Bash conformance | the corpus harness measures `cmd/bashsharp` (`BASHPP_TOOL`); Tour/GbE and the classic OFF/ON lanes measure the built `bashy`/`bash` |
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

- `bashsharp-tests/tools/classic-gate.sh` — Bash 5.3 OFF 86/86 and ON 79 + 7
  (the seven intentional raw reserved-`func` failures).
- `bashsharp-tests/tools/upstream-harness/barrier-run.sh` — the full Go corpus
  replay (≈ 100 min on the certification host); a refactor is green only when
  its barrier equals the previous one **by ID**.
- A Bash#-ON change that is not Class R needs a collision-map row before code.
