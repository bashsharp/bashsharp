# Shell benchmark research: what exists, what to reuse

Story: Sprint 677 #9 (83303c651208). Desk research only: no venue, no runs.
Every claim below was read at its source on 2026-11-21 (raw files / API
metadata fetched; URLs inline). Anything not verified is marked **unverified**.

## 1. Candidates

| # | Candidate | What it measures | License | Reproducibility |
|---|-----------|------------------|---------|-----------------|
| 1 | **Oils `benchmarks/`** — <https://github.com/oils-for-unix/oils/tree/master/benchmarks> | Several shell-vs-shell suites. `osh-runtime.sh`: wall time + rusage of real scripts (`configure` of CPython, util-linux, OCaml, tcc, yash; `abuild -h`; hello-world; `/bin/true`). `osh-parser.sh`: parse time of a pinned file list. `compute.sh`: fib, for_loop, word_freq, bubble_sort, palindrome, parse_help. `startup.sh`: `sh -c 'echo hi'` time and syscall count. `builtin-io.sh`: `wc -l` vs `mapfile` on 1M lines (ad-hoc `time`) | Apache-2.0 (<https://raw.githubusercontent.com/oils-for-unix/oils/master/LICENSE.txt>) | Best of the set. Tarball inputs pinned and fetched from `op.oils.pub/blob/testdata` (tcc-0.9.26, yash-2.46, ocaml-4.06.0, util-linux-2.40; see `osh-runtime.sh`). Raw TSV kept separate from derived stages, plus per-run host and shell provenance records (host-id/shell-id; layout in <https://raw.githubusercontent.com/oils-for-unix/oils/master/benchmarks/README.md>). But: needs Oils' dev shell (`build/dev-shell.sh`, python2 wedge) and R for reports; scripts `source` repo-internal libs, so lift ideas and workload list, not the harness |
| 2 | **Oils `time_.py` + `time-helper.c`** — <https://raw.githubusercontent.com/oils-for-unix/oils/master/benchmarks/time_.py> | A `time` replacement writing TSV rows (elapsed, exit status, rusage, extra fields). Its docstring says why not `/usr/bin/time` (2-digit precision, noisy stderr) and not the bash `time` builtin (no exit code, writes to stderr) | Apache-2.0 (repo) | Small and self-contained; the *design* (one TSV row per run with exit code and run fields) is the reusable part. Written for python2 (own TODO says port to py3) |
| 3 | **Oils spec tests** — `test/sh_spec.py`, `test/spec-runner.sh` (<https://github.com/oils-for-unix/oils/tree/master/test>) | Conformance across bash/dash/mksh/zsh/osh with a per-cell result incl. TIMEOUT, TSV output; spec-runner reports "Elapsed Seconds" per file. Not a benchmark; timing is incidental | Apache-2.0 | Runs against any shell binary; case files are plain-text spec files parsed by `sh_spec.py` |
| 4 | **hyperfine** — <https://github.com/sharkdp/hyperfine> (README: <https://raw.githubusercontent.com/sharkdp/hyperfine/master/README.md>) | Wall time (and optional metrics) of an arbitrary command over repeated runs: default >= 10 runs targeting ~3 s, `--runs`, `--warmup`, `--prepare/--setup/--conclude/--cleanup`, `-L`/`-P` parameter lists/scans, outlier detection, shell-spawn correction, `--export-json/csv/markdown`. Default is no intermediate shell (`-N`/`--shell=none`); `-S` selects `sh`; `--shell <SHELL>` picks one | MIT **or** Apache-2.0 (README "License"; both files present) | Good: JSON export of every run, `scripts/` has Welch t-test, histogram, whisker plots. Single static Rust binary; **not installed on the dev box here** (`which hyperfine` empty), so the venue must provide it. Does not pin inputs or record host provenance: wrap it |
| 5 | **uutils/coreutils `docs/src/performance.md`** — <https://github.com/uutils/coreutils/blob/main/docs/src/performance.md> and `.github/workflows/benchmarks.yml` | Per-utility benches: hyperfine three-way compare (GNU, prev build, new build, `--warmup 3`, `taskset -c 0` for stability) and in-tree Divan/CodSpeed microbenchmarks under `src/uu/*/benches` | MIT | Documented practice we can copy for (a). Divan/CodSpeed benches are Rust-only and not reusable for a Go shell |
| 6 | **brush `benchmarks/bench.py`** — <https://raw.githubusercontent.com/reubeno/brush/main/benchmarks/bench.py> (+ `brush-shell/benches/shell.rs`, criterion) | Shell micro-ops (assignment, `:`, echo builtin vs `/bin/echo`, subshell, cmdsubst, func call, array access, pattern/regex match, if) in a tight loop per shell with warmup, iteration calibration, timed samples; compares test shell vs reference shells. File header says it was AI-agent authored | MIT | Single Python file, JSON output, runs against any shell binary. Closest existing shell-vs-bash *interpreter* micro suite. Not coreutils, not real scripts |
| 7 | **mvdan/sh `syntax/bench_test.go`** — <https://github.com/mvdan/sh> | Go `testing.B` parser benchmarks only | BSD-3-Clause | Go-native; relevant only for parse cost, not exec |
| 8 | **GNU Bash test suite** — `tests/` in <https://git.savannah.gnu.org/cgit/bash.git/tree/tests>; tarballs at <https://ftp.gnu.org/gnu/bash/> | Correctness: `sh run-all` runs every `run-*`, each runs `X.tests` under `${THIS_SH}` and `diff`s against `X.right` (confirmed in `tests/run-all`, `tests/run-alias`, `tests/README`). `run-minimal` runs a fixed subset of the same scripts. **No timing practice**: no timers anywhere in the harness | GPL-3.0 (`COPYING`) | Deterministic given the tarball and `THIS_SH`; default `THIS_SH=../bash`, `TMPDIR` honored. Timing must be wrapped from outside |
| 9 | **yash tests** — <https://github.com/magicant/yash/tree/trunk/tests> | Correctness: `*-p.tst` (POSIX, runnable on any shell: `make TESTEE=<shell> test-posix`) and `*-y.tst` (yash-specific); harness `run-test.sh` compares stdout/stderr/exit status. Some cases skipped by privilege/config; `%REQUIRETTY%` files need a control terminal and cannot run in parallel without a pty. **No timing** | GPL-2.0 (`COPYING`) | Deterministic; `RUN_TEST = ./resetsig $(YASH) ./run-test.sh` in `tests/Makefile.in`. Per-file wall time must be wrapped from outside |
| 10 | **Open POSIX Test Suite (LTP)** — <https://github.com/linux-test-project/ltp/tree/master/testcases/open_posix_testsuite> | Conformance of POSIX *C system interfaces* (IEEE 1003.1-2001), not the shell | GPL-2.0 (its README) | **Not applicable** to shell/utility timing |

Not covered: the Open Group VSC-PCTS shell/utilities suite is proprietary
(**unverified**, not fetched); skipped as unusable for open reproducibility.

Public results to sanity-check against later (not re-verified): the Oils
release pages <https://oils.pub/release/latest/> (reachable on 2026-11-21).

## 2. Findings that matter

- **No official GNU Bash or POSIX suite ships timing.** Both the Bash `run-all`
  and yash `run-test.sh` are pass/fail only (see rows 8 and 9). "Test-suite
  timing" has to be a thin wrapper we write around their unmodified runners.
- **No upstream repeated-run harness for real scripts** exists except Oils'
  `osh-runtime`, and it repeats only via a `num_iters` argument that defaults to 1
  (`osh-runtime.sh`), with one `time-tsv` row per task and no statistics layer,
  and it is tied to Oils' build environment.
- **hyperfine is the de facto repeated-run engine** and is explicitly used by
  uutils for exactly our case (GNU vs alternative coreutils).
- Process-startup cost dominates short commands; Oils' own `startup.sh` and
  hyperfine's `--shell=none` default both exist because of this. For a shell
  whose builtins replace external commands, compare *the same script* under
  each shell, and keep a separate "command through `bash -c`" vs "builtin in a
  loop" split.

## 3. Recommendation: reuse vs build

### (a) Per-command coreutils latency/throughput — **reuse hyperfine, build a thin driver**
- Reuse: hyperfine for repeats, warmup, stats, `-L {sh}` parameter lists, JSON export, as in uutils'
  three-way recipe (GNU / our build / reference). Pin CPU (`taskset -c 0`) where available.
- Build: a small Bash# (or POSIX sh) driver that (1) holds a fixed list of commands + generated pinned inputs (seeded `seq`/`yes`-style data, sized files for throughput), (2) invokes hyperfine once per command with `--shell=none` for process latency and with a `-S`-style loop for in-shell builtin cost, (3) records host/shell provenance next to the JSON (Oils' host-id/shell-id idea), (4) one-row-per-run TSV/JSON schema. No Oils code is needed.
- Do **not** reuse uutils' Divan/CodSpeed benches (Rust-only).
- Throughput cases to borrow from Oils `builtin-io.sh` (`wc -l` vs `mapfile` on 1M lines), but run them under hyperfine rather than bare `time`.

### (b) Official suite timing (Bash 5.3 suite, yash POSIX suite) — **reuse the suites unmodified, build only the timing wrapper**
- Reuse: Bash 5.3 `tests/run-all` (or `run-minimal`) with `THIS_SH` pointing at each shell; yash `make TESTEE=<shell> test-posix` (or `run-test.sh` per `*-p.tst`). GPL: run them, do not vendor them into bench/. Fetch pinned tarballs at run time and record their checksums.
- Build: a wrapper that times each `run-*` / `*-p.tst` file individually (N runs via hyperfine `--runs`, or one run plus per-file wall clock), records pass/fail alongside time, and refuses to publish a time for a red file (Directive #3: a failing test has no benchmark value until root-caused). Handle the known skip/tty caveat for `%REQUIRETTY%` yash files by reporting them as "not timed".
- These timings are mostly process-spawn plus small scripts; treat them as a coarse end-to-end signal, not a micro benchmark.

### (c) Real build/test scripts run N times — **reuse Oils' workload idea and hyperfine; build the corpus**
- Reuse: Oils' `osh-runtime` workload selection (pinned tarballs; `configure` runs of CPython/util-linux/OCaml/tcc/yash; `abuild -h`) as the starter set and the pattern of recording the tarball and shell identity; hyperfine for N-run statistics with `--prepare` to reset the work dir (fresh extract per run, so runs are independent).
- Build: the corpus of bashy's own build/test scripts (inventory is story ad6c920e94c6), with network fetches stubbed and inputs pinned, plus equal-output checking (diff stdout/files between shells, as Oils stores `STDOUT.txt` per task) so a fast-but-wrong run is flagged.
- Do not adopt Oils' build/report stack (python2 wedge, R) — it is the heavy part and not needed.

## 4. Suggested next (not done here)

1. Land hyperfine on the benchmark venue (it is absent on the dev box).
2. Build the driver + TSV/JSON schema once, reuse it for (a), (b), (c).
3. Brush's `bench.py` cases are a cheap extra for shell-core micro ops if wanted; it is orthogonal to this story's three targets.

No benchmarks were run and no venue was touched for this document.
