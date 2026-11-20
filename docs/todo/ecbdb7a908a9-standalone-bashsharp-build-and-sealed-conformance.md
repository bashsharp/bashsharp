---
id: ecbdb7a908a9
kind: task
title: Standalone Bashsharp build and sealed conformance DAG
seq: 6
status: done
priority: p1
created: 2026-11-18T04:53:09.900486Z
assignee: s575-codex-manager
sprint: 575
sprint_id: 65ce85f5-87c0-5863-bf55-62de86f5df36
sprint_title: 'Layer separation: Classic <- Bash# <- Yoke import-graph delineation'
closed: 2026-11-20T12:26:24.776658Z
closed_by: s575-codex-manager
---

Infrastructure only: standalone Dockerfile and DAG build/test/smoke targets using pinned public Bash dependency with GOWORK=off, existing immutable official image and relocated unchanged sealed GNU Bash 5.3 harness. Preserve interpreter, CLI, fixture and test behavior. Build existing language executable and separate pinned Bash shell driver for Bashsharp mode; never claim GNU86 equals licensed POSIX certification. No umbrella or sibling module overlays.

## Manager closeout — 2026-11-20
Standalone DAG/Docker delivery f6f2f29d through 4382d7e is published. Fresh public clone e374985 passed all six Go packages plus helpers; invocation-only followups retain those inputs. Final mode1 GNU86 and both language/shell-driver Docker smoke checks passed. No new test execution or certification claim is inferred.
Existing receipts and source ancestry were reviewed; no new test/build ran during reconciliation. Evidence belongs to its exact historical source/artifact identities, not arbitrary later heads. S575 full-run evidence remains under posix-baseline/s575-2026-11-19 with its raw 4 FAIL, 54 UNRESOLVED and 9 INSPECT unchanged. RC38 is on operator hold.
