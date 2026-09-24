# What Bash# claims — every number with its corpus

The rule this page exists for: **a claim about Bash# that is not a
`bashsharp-tests` result is not a claim.** Every figure below names the
suite it was measured on, the revision, and the host. Numbers are copied
from their status pages, never edited by hand; when a new run lands, this
page changes with it.

Current release: **bashy v0.29.0** (2026-09-24), measured on the
**`sprint-269` reference tag** — bashy `8d8577e`, engine `sh 5d4c0e3d`,
`bashsharp 0d5aeb0`, `coreutils cbf37428`, `yoke 0bf2235`; the release adds
documentation-only commits on top. Re-measured on that tag, on three native
hosts (Linux amd64, macOS arm64, Windows amd64): the Bash 5.3 suite, the
licensed POSIX shell arm, the full Go corpus, Go by Example, A Tour of Go and
the getting-started tour. Rows that were not re-run keep the run they name.

Previous: **bashy v0.28.0** (2026-09-24) — bashy `7416f0b`, engine `sh
72bb8cd`. **v0.27.0** (2026-09-21) — bashy `5220744`, engine `sh 89e98e2d`;
Bash 5.3 86/86 (run 35657932441), tour 6/6 (run 35661066840).

## Claimed

| claim | corpus | result | where measured |
|---|---|---|---|
| Every Bash 5.3 program means the same thing | GNU Bash 5.3's own test suite, every runnable fixture (86), against bashy's own userland | **86/86 on Linux, macOS and Windows** with the dialect off, serial, 0 failures, timeouts or skips; **79 + 7** with it on — the seven are fixtures that name a shell function `func`, listed by name (dialect-on last measured on v0.27.0) | native hosts on `sprint-269`, 2026-09-24; the same 86/86 in the Linux container gate on every `v*` tag |
| POSIX shell behaviour | the licensed VSC-PCTS 2016 shell arm (`POSIX.shell`, 493 test purposes), native Ubuntu, GNU coreutils 9.11 providers | **493/493** in the certification PASS group, 0 FAIL, 0 blockers, 0 caps, 0 runner failures; sealed evidence retrieved and verified | arm `s269-release-shell-20260924` on `sprint-269` (bashy `8d8577e`), 2026-09-24 |
| POSIX shell behaviour, second witness | yash's portable POSIX suite (`*-p.tst`, ~1,840 cases per panel after excluding job-control/signal files for every shell) | v0.23.0: **1832/1833** (Alpine), **1844/1845** (Debian); `main` after v0.23.0 (`sh d41a702b`): **1833/1833 and 1845/1845** — for scale, bash 5.3 scores 96 %, bash 5.2 95 %, yash itself 99 % on the same run | Linux, podman, both reference panels |
| Go 1.27.1 mixed and whole-program support | the upstream Go 1.27.1 test corpus (`test/`, typechecker roots, package roots): 3,497 roots, 3,458 applicable | **3,078 PASS · 380 FAIL · 39 SKIP** (89 %) — PASS means the native oracle passes *and* both Bash# modes (interpreted, and lowered-then-compiled) reproduce it. **Not a full pass**: 257 of the 380 fail only on reviewed exclusions (compiler diagnostics, asmcheck, gc-only checks, unsafe reinterpretation, cgo); 123 are open work | full run on `sprint-269`, 2026-09-24, [`go-corpus-state-2026-09-24.md`](go-corpus-state-2026-09-24.md) (was 2,827 / 631 / 39 on 2026-09-17) |
| Go by Example | all 255 programs, interpreted and compiled | **255/255 on Linux, macOS and Windows** | native hosts on `sprint-269`, 2026-09-24 |
| A Tour of Go | all 97 programs × 3 modes = 291 observations | **291/291 on Linux, macOS and Windows** | native hosts on `sprint-269`, 2026-09-24 |
| The getting-started tour | 40 cases with pinned transcripts (including the two fences chapters) | **40/40 on Linux, macOS and Windows** native hosts, container chapters included; on GitHub's runners every leg passes too, with the two container-engine cases known-failing on the macOS/Windows runner legs only (the hosted runners have no engine; the marker fails the gate if they ever pass) | native hosts on `sprint-269`, 2026-09-24; GitHub's ubuntu/macos/windows runners daily against the latest release |
| Fenced islands need no toolchain on the host | the six island languages (`~~~py` `~~~ts` `~~~rs` `~~~c` `~~~cxx` `~~~go`) and `--source=go` | bashy provisions Go 1.27.1, `zig cc`, a uv-managed CPython 3.13, Node 22 + `typescript@5.9.3`, a rustup toolchain — downloaded from the vendor, checksum-verified, cached; a host tool on `PATH` is never consulted (`BASHPP_*` names one explicitly) | the tour's stripped-PATH leg; from v0.24.0 |
| No-base-image deployment shapes, sized | three required `FROM scratch` images: bashy + `.bsh`, standalone transpilation, and a Python island prepared at build time | all three green — **A** (bashy + `.bsh`) 109,412,490 B / 49,502,560 B gzip; **B** (`transpile --standalone` binary) image 1,994,912 B / 876,537 B gzip, bare binary 1,994,912 B / 873,571 B gzip, SBOM `mvdan.cc/sh/v3 v3.13.1`; **C** (bashy + prepared CPython island; no runtime download) 166,720,052 B / 68,927,404 B gzip | GitHub Actions run [35503564934](https://github.com/qiangli/bashy/actions/runs/35503564934), candidate `bashy 0f73f42`, 2026-09-20 |

## The 380 failing Go roots, honestly

They are published by ID in
[go-corpus-state-2026-09-24.tsv](go-corpus-state-2026-09-24.tsv). **257**
fail only on reviewed exclusions (`go-corpus-exclusions.tsv`: not achievable
by an interpreter — compiler-diagnostic tests, assembly checks, gc-only
checks, `unsafe` reinterpretation, gc observation, cgo; each listed with its
reason and reviewed downward). The other **123** are open work in six
first-cause groups: import resolution (33), interpreter speed at the 60 s
bound (34), native bridge and callbacks (15), language and type gaps (9),
backend scope under review (12), runtime one-offs (20). 22 of the 123 newly
failed since 2026-09-17 and lead their groups. Whole-corpus 3,497/3,497 will
not be claimed.

## Not claimed

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
- Go corpus / Tour / GbE: `bashsharp-tests/tools/upstream-harness/barrier-run.sh` (≈ 100 min).
- The tour: `git clone https://github.com/bashsharp/tour && cd tour && ./check.sh`.
- The VSC shell arm needs the licensed suite; the run's ledger, host manifest and provider list are archived with the release evidence.
- Windows fixture measurement: `cd bashy && scripts/ci-bash53-windows.sh` on
  Windows — it builds the `yoke` userland from the pinned sibling, lays it out
  as the run's POSIX root and prints both the fixture PATH and that root, so the
  count names the userland it was measured against. Scratch images:
  `cd bashy && scripts/quickstart-container-smoke.sh`.
  The authoritative CI runs are linked above.
