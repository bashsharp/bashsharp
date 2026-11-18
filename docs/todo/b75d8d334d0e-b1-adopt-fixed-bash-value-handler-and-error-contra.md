---
id: b75d8d334d0e
kind: refactor
title: B1 Adopt fixed Bash value, handler and error contracts
seq: 4
status: todo
priority: p0
created: 2026-11-18T01:20:34.514614Z
sprint: 575
sprint_id: 65ce85f5-87c0-5863-bf55-62de86f5df36
sprint_title: 'Layer separation: Classic <- Bash# <- Yoke import-graph delineation'
---

Sprint575 shared execution boundary, independently reviewed in umbrella docs/s575-runtime-boundary.md. BLOCKED until B0 cf2ca27023ef is accepted/published. Use accepted A1/A2 foundation github.com/qiangli/bash v0.0.0-20261118005152-72c4bd210f74, exact pin; no foundation source edits, sibling replacements or further lower pin changes to pass failures.

Within owned engine, alias expand ValueKind/Variable/Environ/WriteEnviron and every corresponding value-kind constant (including Object, KeepValue, Unknown/Unset) to foundation identities. Remove duplicate receiver methods on aliased types, including generated ValueKind.String; retain existing public signatures and private environment fixture helpers where necessary. Delegate neutral public constructors where appropriate without turning functions into mutable public variables. Keep B expansion config, AST/parser/Runner and its language behavior owned here. Shared neutral Object/Variable/serialization contracts remain in A; do not delete or redesign them.

Alias interp ExecHandlerFunc, CallHandlerFunc, OpenHandlerFunc, ReadDirHandlerFunc/2, StatHandlerFunc, ExitStatus, SignaledStatus, ValueField/ValueDescription to foundation. Preserve NewExitStatus/IsExitStatus API and unwrap/errors.As behavior. B HandlerContext stays B-private Runner-bound, NOT an alias to A. At every B handler dispatch publish both B private context and adapted A context using A.WithHandlerContext/AdaptHandlerContext, explicit HandlerKind mapping (A HandlerKindExec starts1) and six live captured callbacks DryRun/Async/Umask/SignalIgnored/Builtin/DescribeValue. Convert syntax.Pos using public offset/line/column, invalid remains invalid. Preserve Env/Dir/stdio/Agentic/ExecAs/ExecClearEnv/ExecReplace. Republish A view AFTER exec flag updates in runner.go; a construction-time view is stale. Keep B private redirection/replacement/test markers on the original context chain. B keeps own default exec/open/readdir/stat handlers; never use A defaults on B contexts or mirror private keys. Stat/lstat keep pass-through bare-context behavior, no new kind/wrapping.

Adopt accepted errdetail leaf in engine/polyglot exactly as reviewed A2: alias ErrorDetail, keep private workerError/constructors/render/unwrap in B, replace illegal private receiver helpers on alias with package-local functions preserving logic, carrier returns deep clone and handles nil, public ForeignErrorDetail wrapper delegates neutral extractor. Do not import coreutils/Yoke or add worker implementation to A. Keep existing worker wire/plain/null/empty/deep causes/render/clone/unwrap assertions.

Meaningful focused tests cover A-typed middleware over B Runner contexts for exec/call/open/readdir; live environment/writers/positions/Agentic, all6 callbacks and exec-only Builtin, async/subshell state, exec-a/clear-env/replace updates, A/B status and variable identity, preserved B default-handler behavior/private markers, no new Stat context requirement, neutral A extractor recognizes direct/wrapped B worker errors with copy isolation. Preserve existing corpus and assertions. No changes to upper modules or foundation, unrelated fixes, parser/AST/public Runner redesign, skip/threshold/allowlist weakening or KB.md. Module metadata only exact required A pin; no unrelated upgrades.

ALL builds/tests run in manager's locked official image with GOWORKoff; no local Go build/test/run. Static formatting/metadata operations only. Work in isolated workspace, no pushes/branch switches. Commit with correct Sprint575/Story/Story-ID and actual agent, report exact type/dispatch/error changes and final SHA. Manager independently reviews seam then full standalone B suite/corpus with fixed lower pin. A failure belongs in the B wiring unless baseline-proven otherwise; never alter A to make B tests pass.
