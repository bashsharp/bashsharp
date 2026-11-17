---
id: cf2ca27023ef
kind: refactor
title: B0 Relocate owned Bash# engine and wire language front to it
seq: 3
status: todo
priority: p0
created: 2026-11-17T23:09:37.536406Z
sprint: 575
sprint_id: 65ce85f5-87c0-5863-bf55-62de86f5df36
sprint_title: 'Layer separation: Classic <- Bash# <- Yoke import-graph delineation'
---

Sprint575 B0 minimum-change owned engine snapshot, prerequisite manager acceptance/publication of Bash A0 source. Canonical module remains github.com/bashsharp/bashsharp. Copy frozen A0 packages syntax, expand, interp (except shared interp/ownedexec), internal, lower, gosource, polyglot, grammar and interactive under engine/, preserving source/tests/fixtures/licenses; engine/LICENSE preserves original BSD license alongside existing root MIT. Own all parser/AST/Runner/options/default handlers. Namespace these engine-owned package references to github.com/bashsharp/bashsharp/engine/<pkg>; reuse neutral github.com/qiangli/bash/{execbudget,pathconv,winmode,fileutil,pattern,shell as appropriate} and shared interp/ownedexec rather than duplicating its frame/adoption protocol. Audit shell helpers for AST types: if shell package depends on parser types, copy it to engine too. Do not import foundation internal package from foreign module; use copied engine/internal. All fixture-generated go.mod refs and repo-root discovery must point to real Bashsharp module root and valid v0 placeholder under in-checkout replace; do not alter assertions or unrelated module versions. Mechanically rewire existing front/transpile/cmd/bashsharp to owned B engine, remove old mvdan->qiangli/sh active dependency, require exact accepted published Bash foundation pin supplied by manager. Do NOT implement shared handler aliases/ABI in this slice (A1/B1 follows); do NOT strip A engine, relocate product CLI, touch C/Yoke, change defaults/feature behavior or add features. No source heuristics to fix test failures. Preserve behavior and test inventory; no skips/assertion weakening. Add minimal make-free DAG build/test front door if missing; tests only manager official-image lane, no builds/tests on dev box. Static formatting/source comparison allowed. No native foreign suites, dependency upgrades, KB.md, pushes, umbrella edits or sibling writes. Existing historical source notes can be clarified minimally only where new ownership would be dangerously misleading; no doc rewrite. Commit isolated workspace with actual agent provenance and Sprint/Story/Story-ID. Acceptance: source equivalence after declared reference relocations and root-path adjustments, standalone B build/test with fixed accepted A0 pin. Further shared ABI, Classic stripping and CLI composition are separately reviewed stories.
