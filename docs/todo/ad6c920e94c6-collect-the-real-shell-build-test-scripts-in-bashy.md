---
id: ad6c920e94c6
kind: task
title: Collect the real shell build/test scripts in bashy's third-party apps, toolchains and examples
seq: 10
status: todo
priority: p2
created: 2026-11-21T15:28:30.699277Z
sprint: 677
sprint_id: 334e4662-d75c-5c5f-8671-7b62ea882de1
sprint_title: Fast build, small core binary, and shapeable Yoke CLI modularization
---

Operator 2026-11-21: 'collect all the real shell scripts in the bashy third party apps/toolchains (for building/testing)'; 'we could have another agent to collect the list'; 'instead of fake scripts, run a real set of build/test scripts a specified number of times and compare'; 'rewrite if needed depending on what they are'. Inventory only, no runs: walk bashy builds/*/ (fork build/test scripts, render/audit scripts), toolchain/ provisioner scripts, examples/*/ scripts, scripts/ build/test lanes. For each script: path, purpose, interpreter (bash/sh), length, external commands used, whether it runs offline and repeatably as-is, what a minimal rewrite would need (stub network fetch, pin input) and why. Deliver bashsharp bench/scripts-inventory.md (+ a machine-readable list) and propose a starter set of 10-20 representative scripts for N-times runs under bashy vs GNU bash in containers on the gate host. No hostnames in committed text.
