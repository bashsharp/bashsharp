---
id: cb5d2c132586
kind: refactor
title: S575 B2 relocate existing shell CLI without implementation changes
seq: 5
status: done
priority: p0
created: 2026-11-18T04:05:25.895702Z
assignee: s575-codex-manager
sprint: 575
sprint_id: 65ce85f5-87c0-5863-bf55-62de86f5df36
sprint_title: 'Layer separation: Classic <- Bash# <- Yoke import-graph delineation'
closed: 2026-11-20T12:26:24.166052Z
closed_by: s575-codex-manager
---

Operator correction: separate repository source before any sh internals refactoring. Copy existing bashy/internal/cli unchanged to bashsharp/cli; relocate its private interactiveprobe helper unchanged; modify only imports and required module references. Preserve all implementation and test bodies. Run go build ./... in the locked official image with GOWORK=off. A4 stripping excluded; no cleanup or redesign.

## Manager closeout — 2026-11-20
Shared CLI and helper relocation published in 4295cf1e/59ce327a. Existing six Go packages and helper/build qualification passed under the official standalone layout. This accepts the original reference-only move; later explicitly authorized CLI options and the future engine split are separately recorded.
Existing receipts and source ancestry were reviewed; no new test/build ran during reconciliation. Evidence belongs to its exact historical source/artifact identities, not arbitrary later heads. S575 full-run evidence remains under posix-baseline/s575-2026-11-19 with its raw 4 FAIL, 54 UNRESOLVED and 9 INSPECT unchanged. RC38 is on operator hold.
