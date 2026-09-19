# Naming: the language is Bash# (`bashsharp`), not Bash++ (`bashpp`)

Decided 2026-09-18 (Sprint 212 D3, executed in Sprint 211). **Bash++ was
already taken**: [rail5/bashpp](https://github.com/rail5/bashpp) predates
this project and owns the name, the `bashpp` slug, the `bpp` binary, the
`.bpp` extension, a language spec and `bpp.sh`. Two languages called Bash++
with two `.bpp` extensions would mislead every reader and every package
index, so this one is renamed **Bash#** ("bash sharp"):

| surface | Bash# (canonical) | Bash++ era (deprecated alias, one minor release) |
|---|---|---|
| repos | `bashsharp/bashsharp`, `bashsharp/bashsharp-tests`, `bashsharp/bashsharp-tour` (org, 2026-09-19) | GitHub redirects from `qiangli/bashpp`, `qiangli/bashpp-tests` and `qiangli/bashsharp*` — never recreate a repo at any of those names |
| module | `github.com/bashsharp/bashsharp` | `github.com/qiangli/bashsharp` (pre-org path, one Sprint; the redirect still resolves it) |
| binary | `cmd/bashsharp` → `bashsharp` | — |
| script extension | `.bsh` | `.bpp` (warns) |
| invocation flag | `--bashsharp` / `--no-bashsharp` | `--bashpp`, `--bash++`, `--no-bashpp` (warn) |
| environment | `BASHY_BASHSHARP=1\|0` | `BASHY_BASHPP` (warns; loses to `BASHY_BASHSHARP` when both are set) |
| shell option | `set -o bashsharp` | `set -o bashpp` (accepted; `set -o` still lists `bashpp` this release) |
| shebang | `#!/usr/bin/env -S bashy --bashsharp` | |

**What is NOT renamed, deliberately.** The `sh` engine's identifiers —
`syntax.LangBashPP`, `interp/bashpp_*.go`, the `bashPP*` methods, the
`BASHPP-E…` diagnostic codes, the `BASHPP_*` harness environment variables
in `bashsharp-tests`, and the `BashPP*` Go identifiers in this module's
`front` package. They are not public surface; the grammar is frozen (D2 of
Sprint 211); ~140 engine files would move for no user-visible gain. Prose
in dated design documents keeps "Bash++" as the name of record at the time.

"Bash#" was previously the name of the ergonomics tier inside the language
(`bashsharp-ergonomics-tier.md`); that tier keeps its content and drops its
separate name — it is simply part of Bash#.
