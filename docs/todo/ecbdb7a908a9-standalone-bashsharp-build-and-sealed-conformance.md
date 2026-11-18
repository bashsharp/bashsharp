---
id: ecbdb7a908a9
kind: task
title: Standalone Bashsharp build and sealed conformance DAG
seq: 6
status: assigned
priority: p1
created: 2026-11-18T04:53:09.900486Z
assignee: s575-codex-manager
sprint: 575
sprint_id: 65ce85f5-87c0-5863-bf55-62de86f5df36
sprint_title: 'Layer separation: Classic <- Bash# <- Yoke import-graph delineation'
---

Infrastructure only: standalone Dockerfile and DAG build/test/smoke targets using pinned public Bash dependency with GOWORK=off, existing immutable official image and relocated unchanged sealed GNU Bash 5.3 harness. Preserve interpreter, CLI, fixture and test behavior. Build existing language executable and separate pinned Bash shell driver for Bashsharp mode; never claim GNU86 equals licensed POSIX certification. No umbrella or sibling module overlays.
