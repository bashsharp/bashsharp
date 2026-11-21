---
id: fa807380a5b2
kind: test
title: 'Benchmark bsh vs GNU bash + GNU coreutils: small-call-heavy scripts and large-data throughput'
seq: 8
status: todo
priority: p3
created: 2026-11-21T15:15:01.114219Z
---

Operator 2026-11-21: file a todo for the bsh vs GNU benchmark (after everything works; not planned now). First data point (dev host, macOS arm64, 2026-11-21): a 500-iteration loop with basename, echo|tr|cut and two command substitutions, same output: bsh (in-process coreutils, empty PATH) 0.22 s; GNU bash 5.3 + GNU coreutils 2.46 s; GNU bash 5.3 + BSD utils 1.91 s (bsh ~11x faster). Startup (-c true, median of 20): bsh 21 ms vs bashy 56 ms. Hypothesis: bsh wins on small-call-heavy scripts (no fork/exec per utility; in-process command substitution/pipelines) but may lose on large-data single-call throughput (sort/grep/awk over GB) where GNU C per-byte speed dominates. Scope when picked up: a reproducible benchmark suite (script shapes: utility loops, pipelines, command substitution, startup; data shapes: sort/grep/awk/sed/wc on 10 MB-10 GB) run on Linux/macOS/Windows on the gate host, bsh built through the product build path, GNU versions recorded; publish per-shape results without cherry-picking. Related: Bash# build performance todo 7922c8e2.

## Operator 2026-11-21: per-command coverage, start now

Operator (typed): "the benchmark should cover metrics for each command in the coreutils which should be straight forward. why not assign one agent to do it now?"

SCOPE NOW: for EVERY coreutils command bsh carries (the POSIX-required set plus anything else linked), measure against its GNU coreutils equivalent (skip commands with no GNU equivalent, listed):
- per-call latency: one invocation on tiny input, median of N, inside a bsh script vs inside GNU bash 5.3 with GNU coreutils on PATH (fork/exec included);
- throughput: one invocation on 10 MB and 1 GB inputs where the command processes data (sort, grep, sed, awk, wc, cut, tr, head/tail, uniq, cat, od, cksum, ...);
- correctness guard: outputs must match (or the difference is recorded); never report speed for a mismatching run.
Output: one table per command (latency and throughput, bsh vs GNU, ratio), plus a summary of commands where bsh is slower. Results and the scripts go in bashsharp under bench/ (reproducible). Venue: the Linux gate host in elephant.lane3 ONLY (lane1/lane2 stay free for the S677 integration run), official image, session attached; macOS/Windows later. bsh built from bashsharp/cmd/bsh at origin/main; record GNU versions. Measurement only: no fixes.
