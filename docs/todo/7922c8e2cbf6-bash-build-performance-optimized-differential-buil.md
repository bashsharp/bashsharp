---
id: 7922c8e2cbf6
kind: idea
title: 'Bash# build performance: optimized / differential builds of Bash# adapters and the commands they build (rebuild only what a change touches)'
seq: 7
status: todo
priority: p3
created: 2026-11-21T15:05:46.345386Z
---

Operator 2026-11-21: 'we may need in future to do optimized build/differential' ... 'this will be bash# build performance/optimization'. Filed for the future; not planned now. Context: S677 makes every non-internal bashy command a separately built unit behind a few-line Bash# adapter; the warm per-command rebuild target is < 10 s, ideally < 5 s, because build time is the software-shaping feedback loop. Inputs when picked up: S677's per-command build-time measurement (per group median/p90/max, slowest commands and dominant dependencies). Directions to evaluate: shared warm Go build cache across adapter builds; content-hash skip when inputs are unchanged; prebuilt binaries for stable commands; dependency diet for outliers; incremental link. Related backlog: Bash# post-v1.0 interpreted compute performance. (Moved from bashy todo c7268f72.)
