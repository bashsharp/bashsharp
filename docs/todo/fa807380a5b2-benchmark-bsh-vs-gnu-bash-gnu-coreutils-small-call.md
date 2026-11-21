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

## Venue correction 2026-11-21 (operator: "on macos")

Run on macOS: the macOS QA host (claim it first, release after; work in a fresh per-run directory there), NOT the developer workstation (no heavy runs there) and not the Linux gate host. bsh built for darwin from bashsharp/cmd/bsh at origin/main; GNU bash 5.3 + GNU coreutils as installed on that host (record versions and paths; if GNU coreutils is absent there, stop and report rather than installing system packages). Name the host by role only in anything committed.

## Venues (operator 2026-11-21, supersedes the venue lines above)

Run the same per-command benchmark on three OSes, one venue each, claiming each before use and releasing after:
- macOS: the macOS QA host. Install GNU coreutils there with Homebrew (`brew install coreutils`; GNU tools under its gnubin directory, system tools untouched) - operator-approved.
- Windows: the gate host's native Windows lane (claim `elephant.windows-native`), not WSL. Baseline = Git for Windows bash + its bundled GNU coreutils; record versions; commands it lacks are listed as no-baseline.
- Linux: a NEW throwaway DigitalOcean droplet (smallest size that runs the 1 GB throughput cases), created for this run and DESTROYED when done (verify deletion); tag it with the story id; never touch existing droplets or bashy.dhnt.io; DigitalOcean via curl with the vault token (doctl ignores tokens). Install GNU bash 5.3 + GNU coreutils from the distro.
bsh: built per OS from bashsharp/cmd/bsh at origin/main (cross-build where Go is absent). Results per OS in bench/results/<os>/; one summary table across OSes. Hosts by role only in committed text.
