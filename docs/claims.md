# What Bash# claims — every number with its corpus

The rule this page exists for: **a claim about Bash# that is not a
`bashsharp-tests` result is not a claim.** Every figure below names the
suite it was measured on, the revision, and the host. Numbers are copied
from their status pages, never edited by hand; when a new run lands, this
page changes with it.

Current release: **bashy v0.23.0** (2026-09-18) — bashy `1c66439`, engine
`sh acbaef8d`, `coreutils 4f4e3507`, `yoke 2854c03`, `bashsharp 4167be8`.

## Claimed

| claim | corpus | result | where measured |
|---|---|---|---|
| Every Bash 5.3 program means the same thing | GNU Bash 5.3's own test suite, every runnable fixture (86) | **86/86** with the dialect off; **79 + 7** with it on — the seven are fixtures that name a shell function `func`, listed by name | native macOS, serial, on the tagged commit; the same 86/86 in the Linux container gate on every `v*` tag |
| POSIX shell behaviour | the licensed VSC-PCTS 2016 shell arm (`POSIX.shell`, 493 test purposes), native Ubuntu, GNU coreutils 9.11 providers | **493/493** in the certification PASS group, 0 FAIL, 0 blockers, 0 caps | run `bash-system-20260918T145814Z` on the v0.23.0 commit; identical to the 2026-08-08 milestone |
| POSIX shell behaviour, second witness | yash's portable POSIX suite (`*-p.tst`, ~1,840 cases per panel after excluding job-control/signal files for every shell) | v0.23.0: **1832/1833** (Alpine), **1844/1845** (Debian); `main` after v0.23.0 (`sh d41a702b`): **1833/1833 and 1845/1845** — for scale, bash 5.3 scores 96 %, bash 5.2 95 %, yash itself 99 % on the same run | Linux, podman, both reference panels |
| Go 1.27.1 mixed and whole-program support | the upstream Go 1.27.1 test corpus (`test/`, typechecker roots, package roots): 3,497 roots, 3,458 applicable | **2,827 PASS · 631 FAIL · 39 SKIP** — PASS means the native oracle passes *and* both Bash# modes (interpreted, and lowered-then-compiled) reproduce it | Barrier D, 2026-09-17, `go-corpus-state-2026-09-17.md` |
| Go by Example | all 255 programs | **255/255** | same revisions as Barrier D |
| A Tour of Go | all 291 programs | **291/291** | same revisions as Barrier D |
| The getting-started tour | 29 cases with pinned transcripts | **all chapters pass on Linux, macOS and Windows** against the latest release (the badge on [tour](https://github.com/bashsharp/tour)), on two legs per OS — the runner's toolchains on `PATH`, and every toolchain stripped off it — with identical transcripts; nothing skipped, no known-failing cases | GitHub's ubuntu/macos/windows runners, daily; from v0.24.0 |
| Fenced islands need no toolchain on the host | the six island languages (`~~~py` `~~~ts` `~~~rs` `~~~c` `~~~cxx` `~~~go`) and `--source=go` | bashy provisions Go 1.27.1, `zig cc`, a uv-managed CPython 3.13, Node 22 + `typescript@5.9.3`, a rustup toolchain — downloaded from the vendor, checksum-verified, cached; a host tool on `PATH` is never consulted (`BASHPP_*` names one explicitly) | the tour's stripped-PATH leg; from v0.24.0 |

## The 631 failing Go roots, honestly

They are published by ID in [go-corpus-targets.md](go-corpus-targets.md)
with one of four classes: **repair 284** (a defect with a first cause — the
`good first issue`s), **review 6**, **blocked-design 76** (needs a design,
never excluded: multi-package interpreted execution, interpreter per-call
cost, retained callbacks, …), **excluded 265** (not achievable by an
interpreter: compiler-diagnostic tests, assembly checks, `unsafe`
reinterpretation, cgo, …; every one listed with its reason, reviewed
downward). Whole-corpus 3,497/3,497 will not be claimed.

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

## How to reproduce any row

- Bash 5.3: `cd bashy && make test-bash` (serial; needs a controlling terminal).
- yash POSIX: `cd bashy && scripts/yash-posix-suite.sh` (needs a container runtime).
- Go corpus / Tour / GbE: `bashsharp-tests/tools/upstream-harness/barrier-run.sh` (≈ 100 min).
- The tour: `git clone https://github.com/bashsharp/tour && cd tour && ./check.sh`.
- The VSC shell arm needs the licensed suite; the run's ledger, host manifest and provider list are archived with the release evidence.
