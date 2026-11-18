---
id: cb5d2c132586
kind: refactor
title: S575 B2 relocate existing shell CLI without implementation changes
seq: 5
status: assigned
priority: p0
created: 2026-11-18T04:05:25.895702Z
assignee: s575-codex-manager
sprint: 575
sprint_id: 65ce85f5-87c0-5863-bf55-62de86f5df36
sprint_title: 'Layer separation: Classic <- Bash# <- Yoke import-graph delineation'
---

Operator correction: separate repository source before any sh internals refactoring. Copy existing bashy/internal/cli unchanged to bashsharp/cli; relocate its private interactiveprobe helper unchanged; modify only imports and required module references. Preserve all implementation and test bodies. Run go build ./... in the locked official image with GOWORK=off. A4 stripping excluded; no cleanup or redesign.
