---
id: 83303c651208
kind: spike
title: Research existing shell benchmarks and harnesses to reuse for bsh vs GNU bash
seq: 9
status: done
priority: p2
created: 2026-11-21T15:28:30.559474Z
weave: 5
assignee: agy-sonnet5.5
sprint: 677
sprint_id: 334e4662-d75c-5c5f-8671-7b62ea882de1
sprint_title: Fast build, small core binary, and shapeable Yoke CLI modularization
closed: 2026-11-21T16:07:29.826332Z
resolution: fixed
closed_by: agy-sonnet5.5-e
---

Operator 2026-11-21: 'may there are official scripts benchmarking. so a research first?' Desk research only, ~30 min, no venue, no runs. Survey published shell benchmark suites and harnesses (for example the Oils/osh project's shell benchmarks, hyperfine-style repeated-run timing, any GNU bash or POSIX test-suite timing practice), verify each claim at its source. Deliver a short doc in bashsharp bench/RESEARCH.md: candidates, what each measures, license, reproducibility, and a recommendation of what to reuse vs build for (a) per-command coreutils latency/throughput, (b) official test-suite timing (GNU Bash 5.3 suite, yash POSIX suite), (c) real build/test scripts run N times. Cite URLs.
