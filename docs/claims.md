# What Bash# claims — every number with its corpus

The rule this page exists for: **a claim about Bash# that is not a
`bashsharp-tests` result is not a claim.** Every figure below names the
suite it was measured on, the revision, and the host. Numbers are copied
from their status pages, never edited by hand; when a new run lands, this
page changes with it.

The separately labelled candidate agent-outcome experiment below is an
exception: its measurements are copied from reviewed `agent-bench` evidence
and do not replace the release conformance claims.

Current release: **bashy v0.29.0** (2026-09-24), measured on the
**`sprint-269` reference tag** — bashy `8d8577e`, engine `sh 5d4c0e3d`,
`bashsharp 0d5aeb0`, `coreutils cbf37428`, `yoke 0bf2235`; the release adds
documentation-only commits on top. Re-measured on that tag, on three native
hosts (Linux amd64, macOS arm64, Windows amd64): the Bash 5.3 suite, the
licensed POSIX shell arm, the full Go corpus, Go by Example, A Tour of Go and
the getting-started tour. Rows that were not re-run keep the run they name.

The two Go rows below were re-measured later, on the **Sprint 376 final
candidate** (`sh 0e1b00108`, language front end `2389bc2`, Linux amd64,
2026-10-05) and then extended by Sprint 379 (`sh 2a6d42599`, the Go island
entry contract). Their 2026-09-24 figures are kept under
[Historical Go figures](#historical-go-figures-superseded) and are not
current.

Previous: **bashy v0.28.0** (2026-09-24) — bashy `7416f0b`, engine `sh
72bb8cd`. **v0.27.0** (2026-09-21) — bashy `5220744`, engine `sh 89e98e2d`;
Bash 5.3 86/86 (run 35657932441), tour 6/6 (run 35661066840).

## Claimed

| claim | corpus | result | where measured |
|---|---|---|---|
| Every Bash 5.3 program means the same thing | GNU Bash 5.3's own test suite, every runnable fixture (86), against bashy's own userland | **86/86 on Linux, macOS and Windows** with the dialect off, serial, 0 failures, timeouts or skips; **79 + 7** with it on — the seven are fixtures that name a shell function `func`, listed by name (dialect-on last measured on v0.27.0) | native hosts on `sprint-269`, 2026-09-24; the same 86/86 in the Linux container gate on every `v*` tag |
| POSIX shell behaviour | the licensed VSC-PCTS 2016 shell arm (`POSIX.shell`, 493 test purposes), native Ubuntu, GNU coreutils 9.11 providers | **493/493** in the certification PASS group, 0 FAIL, 0 blockers, 0 caps, 0 runner failures; sealed evidence retrieved and verified | arm `s269-release-shell-20260924` on `sprint-269` (bashy `8d8577e`), 2026-09-24 |
| POSIX shell behaviour, second witness | yash's portable POSIX suite (`*-p.tst`, ~1,840 cases per panel after excluding job-control/signal files for every shell) | v0.23.0: **1832/1833** (Alpine), **1844/1845** (Debian); `main` after v0.23.0 (`sh d41a702b`): **1833/1833 and 1845/1845** — for scale, bash 5.3 scores 96 %, bash 5.2 95 %, yash itself 99 % on the same run | Linux, podman, both reference panels |
| **Go language constructs in Bash# scripts** — Go mixed into a `.bsh`, and package-led Go units, interpreted by the Bash# engine | Tour of Go (97 programs) and Go by Example (85 programs), each in three modes (native `go`, interpreted Bash#, lowered-then-compiled Bash#); the upstream Go 1.27.1 corpus (3,497 roots, 3,458 native-applicable) as a stress test; the [Go delta table](go-delta.md) | **Tour 97/97 and Go by Example 85/85 in all three modes.** Corpus, interpreted: **3,177 PASS · 281 FAIL · 39 SKIP**, every one of the 281 a named, reviewed exclusion by root ID (breakdown below); lowered-then-compiled: **3,458/3,458 PASS**; native oracle **3,458/3,458**. **Not a full pass**, and no universal Go percentage is inferred from a corpus denominator. Constructs that differ from Go are published as a table (26 rows: 12 supported, 3 limited, 4 gap, 7 excluded), not as a score | Tour, Go by Example and corpus: full run `s376-final-m8`, Linux amd64, `sh 0e1b00108`, 2026-10-05, [`go-corpus-state-2026-10-05.md`](go-corpus-state-2026-10-05.md); delta table: the 26 rows are proven by `bashsharp-tests/tools/go-delta-gate.sh` (26/26 on one local macOS arm64 build, 2026-10-08; see [Go delta](#go-delta-table)) |
| **Fenced or embedded Go source** — Go-only source entered through `~~~go`, `embed go "./file.go"`, a `.go` file or `--source=go`, compiled by the provisioned Go toolchain | the island rows of the [Go delta table](go-delta.md) (I01–I07): a program fence, a function fence behind an alias, an embedded program, a whole `.go` file, and the refusals (program with an alias, non-`main` package, no `go.mod`) | **7/7 island rows pass** their fixtures. This claim covers Go compiler execution plus Bash#'s entry, build and bridge behaviour; it does **not** say the Bash# interpreter executes a whole Go module, and no interpreted whole-Go-source mode is claimed. A compiled island result earns no interpreted-script credit | `bashsharp-tests/tests/go-delta/` via `tools/go-delta-gate.sh`, on `sh 2a6d42599` (Sprint 379 S3), 2026-10-08, one local macOS arm64 build; **not yet repeated on Linux or Windows** |
| The getting-started tour | 40 cases with pinned transcripts (including the two fences chapters) | **40/40 on Linux, macOS and Windows** native hosts, container chapters included; on GitHub's runners every leg passes too, with the two container-engine cases known-failing on the macOS/Windows runner legs only (the hosted runners have no engine; the marker fails the gate if they ever pass) | native hosts on `sprint-269`, 2026-09-24; GitHub's ubuntu/macos/windows runners daily against the latest release |
| Fenced islands need no toolchain on the host | the six island languages (`~~~py` `~~~ts` `~~~rs` `~~~c` `~~~cxx` `~~~go`) and `.go` / `--source=go` (compiled, not interpreted, since Sprint 379) | bashy provisions Go 1.27.1, `zig cc`, a uv-managed CPython 3.13, Node 22 + `typescript@5.9.3`, a rustup toolchain — downloaded from the vendor, checksum-verified, cached; a host tool on `PATH` is never consulted (`BASHPP_*` names one explicitly) | the tour's stripped-PATH leg; from v0.24.0 |
| No-base-image deployment shapes, sized | three required `FROM scratch` images: bashy + `.bsh`, standalone transpilation, and a Python island prepared at build time | all three green — **A** (bashy + `.bsh`) 109,412,490 B / 49,502,560 B gzip; **B** (`transpile --standalone` binary) image 1,994,912 B / 876,537 B gzip, bare binary 1,994,912 B / 873,571 B gzip, SBOM `mvdan.cc/sh/v3 v3.13.1`; **C** (bashy + prepared CPython island; no runtime download) 166,720,052 B / 68,927,404 B gzip | GitHub Actions run [35503564934](https://github.com/qiangli/bashy/actions/runs/35503564934), candidate `bashy 0f73f42`, 2026-09-20 |

## Candidate experiment — reviewed configuration result

This separate agent-outcome measurement comes from `agent-bench`, not the
release conformance suites above. It does not replace or remeasure any release
claim. The final scalar evidence has passed independent review.

| claim | corpus | result | candidate and public evidence |
|---|---|---|---|
| No benefit in this frozen shell-interface configuration | 20 L2 tasks × K=3 × three arms; same 60 s budget, `gpt-6-luna` low, Codex CLI 0.157.1 | Same Bashy binary with `--no-bashpp` **59/60**, guards **13/60**, fences **12/60**; guards minus Bash **−76.67 pp [−88.42, −64.91]**, fences minus Bash **−78.33 pp [−88.85, −67.82]** (paired normal 95% CI) | Binary SHA-256 `52e67841c1972dcb1d447ebf9271cd1037200adc2644877746267fe8b2a25040`; [scalar results](https://github.com/qiangli/agent-bench/blob/main/experiments/bashsharp-l2/results/sprint-381.json), [protocol](https://github.com/qiangli/agent-bench/blob/main/experiments/bashsharp-l2/README.md) |

Limits: 184 attempts include four retained infrastructure voids and 180 valid
outcomes. Intervals pair 60 task-repetition outcomes, without clustering by
20 tasks. Sixteen unknown-usage rows make treatment token-per-solve figures
lower bounds; dollar cost is unknown. Effect-cap denial appeared in 53 guards
and 47 fences trials: a configuration acceptance/capability-integration problem,
not a general language-efficacy finding or proof of safety. The frozen runtime
did not include S7/S8 fleet rows; this is not an S8 full-harness pass.
[Full comparison, costs and failure taxonomy](bashsharp-for-agents.md#what-the-paired-experiment-measured).

## The 281 interpreted Go failures, honestly

They are published by root ID in
[go-corpus-state-2026-10-05.tsv](go-corpus-state-2026-10-05.tsv) (keyed table,
every root and mode) and
[go-corpus-state-2026-10-05-compare.tsv](go-corpus-state-2026-10-05-compare.tsv)
(against the previous record). All 281 fail the *interpreted* mode only; the
same roots pass native and lowered-then-compiled. None is unexplained: there
are **0 new failures, 0 regressions and 0 missing IDs**, and 0 failing rows
outside the exclusion catalog (`go-corpus-exclusions.tsv`).

| why the root is excluded from the interpreted claim | roots |
|---|---:|
| compiler diagnostics (the test asserts a `gc` error message) | 108 |
| assembly checks (`asmcheck`) | 75 |
| gc-only checks | 30 |
| `unsafe` memory reinterpretation (no native byte layout for interpreter-owned values; operator decision 2026-10-04) | 20 |
| gc observation | 16 |
| compute-bound: correct at full scale, slower than the 60 s acceptance limit | 11 |
| cgo | 8 |
| whole-package Go source: the compiled path is the supported one (operator decision 2026-10-04) | 9 |
| assembly input | 4 |
| **total** | **281** |

The 11 compute-bound roots were each run to completion with a longer
diagnostic budget and matched the native output
([interpreted-performance-v1.md](interpreted-performance-v1.md)); that is
correctness evidence, not a pass of the 60 s gate, and optimization is
post-v1.0. Whole-corpus 3,497/3,497 will not be claimed.

What "compiled" means in these corpus rows: the lowered-then-compiled mode
lowers a script to Go and builds it. It is a second measurement of the
*script* claim, not the island route, so it earns the island claim nothing.

## Go delta table

[`go-delta.md`](go-delta.md) is the single source of truth for every Go
construct that differs from Go: one row per construct, keyed by Go spec
section and grammar production, with status, reason, workaround, the
diagnostic the engine prints and the fixture that proves the row. It is
rendered twice from one file (`godelta/go-delta.tsv`): the page for people,
and `bashy explain go` (`--json`, `--tsv`) for tools;
`bashy explain go "<refusal text>"` resolves a refusal to its row and its
workaround. Reproduce: build `bashy`, then
`BASHY_BIN=…/bashy bashsharp-tests/tools/go-delta-gate.sh`.

## Historical Go figures (superseded)

Measured on the `sprint-269` tag, 2026-09-24
([go-corpus-state-2026-09-24.md](go-corpus-state-2026-09-24.md)); kept as
dated history, replaced by the rows above:

- one combined row "Go 1.27.1 mixed and whole-program support": 3,078 PASS ·
  380 FAIL · 39 SKIP of 3,458 applicable roots (257 reviewed exclusions, 123
  open), under a "PASS means both Bash# modes reproduce the oracle" reading
  that mixed the script claim with whole-program claims;
- Go by Example 255/255 observations (85 programs × 3 modes) and A Tour of Go
  291/291 observations (97 × 3), each on Linux, macOS and Windows.

## Not claimed

- **A Go percentage.** No "N % of Go" is stated or implied for either Go
  claim; the delta table lists what differs. A corpus denominator counts
  roots, not language coverage.
- **That the interpreter runs Go-only source.** `.go` files, `--source=go`,
  `~~~go` and `embed go` are compiled by the provisioned toolchain.
- **Corpus "compiled" results as island results.** See the failures section.
- **A multi-package or module-resolution island gate.** The island claim is
  the seven delta rows above; a dedicated multi-package compiled gate has not
  been built.
- **Additional `unsafe` emulation.** Existing tested emulation may remain; none
  is owed or added.
- **"100 % Go"** or **"Go 1.27 conformant."** What is supported is an
  enumerated profile (`go1.27-profile-v1`), measured root by root.
- **"POSIX certified."** The 493/493 is a conformance *pre-flight* on the
  licensed suite; certification is a separate process that has not been
  completed.
- **"100 % drop-in"** without the qualifier: "on Bash's own 86 fixtures,
  dialect off." Job control and some interactive corners are incomplete; the
  bashy README lists them.
- **Speed.** bashy is roughly 2× slower than GNU bash on microbenchmarks.
  Not a goal of this release.
- **That the interpreter calls a model.** It never does. `agentic` is a
  boundary plus an exit status; the demo runs offline.
- **That the name is unique.** It was Bash++; rail5's Bash++ had the name
  first, so it is Bash#.
- **That a provisioned toolchain is bundled or offline.** It is downloaded
  once, on first use, from the vendor's release and verified against a
  digest pinned in bashy's source; the `rustup` toolchain is `stable` at the
  time of that first install, not a fixed version. `bashy check --prepare`
  is the way to pay that download ahead of an offline run.
- **Full Bash compatibility from the 86-fixture result.** The current
  candidate passes every runnable fixture on the measured platforms. The
  fixture corpus does not cover every interactive or external-command behavior;
  the bashy README records the remaining scope. Earlier Windows gaps and their
  repairs are documented in the umbrella's Sprint 245, 246, and 253 evidence.
- **A scratch-image deploy as sandboxed.** The three green images above are
  a smaller *supply-chain* surface — one Go module graph, no distro layer —
  not an isolation boundary; an island still runs with the task's host
  authority, and the ten pinned external POSIX providers (`m4 man ctags ar
  nm strip ex vi lp localedef`) still apply if a script calls one.

## How to reproduce any row

- Bash 5.3: `cd bashy && make test-bash` (serial; needs a controlling terminal).
- yash POSIX: `cd bashy && scripts/yash-posix-suite.sh` (needs a container runtime).
- Go corpus / Tour / GbE: `bashsharp-tests/tools/upstream-harness/barrier-run.sh` (≈ 100 min; the 2026-10-05 run took about 2 h on 4 vCPUs).
- Go delta rows: `bashsharp-tests/tools/go-delta-gate.sh` (needs a built `bashy` and a provisionable Go toolchain).
- The tour: `git clone https://github.com/bashsharp/tour && cd tour && ./check.sh`.
- The VSC shell arm needs the licensed suite; the run's ledger, host manifest and provider list are archived with the release evidence.
- Windows fixture measurement: `cd bashy && scripts/ci-bash53-windows.sh` on
  Windows — it builds the `yoke` userland from the pinned sibling, lays it out
  as the run's POSIX root and prints both the fixture PATH and that root, so the
  count names the userland it was measured against. Scratch images:
  `cd bashy && scripts/quickstart-container-smoke.sh`.
  The authoritative CI runs are linked above.
