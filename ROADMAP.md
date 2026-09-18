# Bash# roadmap

Bash# is **alpha** (bashy 0.x). This page says which parts are settled,
which are open, and how a change gets in. Numbers live in
[docs/claims.md](docs/claims.md); this page never repeats them.

## The five clauses, by stage

| clause | stage | what "done" means | what is open |
|---|---|---|---|
| 1 Base — Bash 5.3 superset | **stable** | 86/86 with the dialect off; every admitted shape is a *start site* with a measured collision class | nothing planned; bug fixes only |
| 2 Standard — POSIX | **stable** | the licensed shell arm and yash's POSIX suite; inert under `--posix` | certification is a separate process |
| 3 Typed core — Go mixed | **alpha** | 2,827/3,497 upstream roots both modes; Tour + GbE complete | 296 repairable roots (good first issues); multi-package interpreted execution and interpreter per-call cost (design calls); **top-level Go `if`/`for` blocks in shell text** (planned — inside a `func` they work); a call in keyword-argument position; negative literals as call arguments |
| 4 Polyglot — fenced islands | **alpha** | python · typescript · rust · c/c++ · go · bash/sh islands as callables | Windows toolchains (Store python alias, MSVC linking, clang SDK paths); `--source=go` should use bashy's own provisioned Go |
| 5 Agentic — `agentic` + contracts | **alpha, syntax open** | the scope rule, `@require`/`@ensure`/`@guard`, yield = 6, decorators, advice | the canonical written form of an `agentic` action across files; what a harness should do with exit 6 (an RFC with Claude Code / OpenCode / Codex in the room) |
| Sharp tier | **alpha, syntax open** | decorators, keyword/default args, exhaustive enums, deep `readonly`, null-safety check | the standard decorator set (`trace`, `guard`, `retry`, …); enum syntax; what `readonly` freezes across a subshell; a `(T, error)` return in shell text |

Also planned, not clause-bound: `transpile --standalone` (the emitted Go
imports the engine's runtime, which needs a `qiangli/sh` checkout today);
carrying host UTF-8 locales beyond `C.UTF-8` and the macOS default.

## Toward 1.0

1.0 means: the five clauses' *syntax* is frozen (a shape admitted in 1.0 is
admitted forever), the Sharp tier's spellings are settled, the `agentic`
canonical form is written, and every repairable Go root is either fixed or
moved to a class with a reason. It does not mean 3,497/3,497 or POSIX
certification.

## How a change gets in

- **A bug** (a Bash 5.3 program that means something different; a POSIX
  case; a corpus root in the *repair* class): open an issue with the
  reproducer, or pick a `good first issue` in
  [bashsharp-tests](https://github.com/qiangli/bashsharp-tests) — each carries
  the failing root and its oracle diff.
- **A syntax change** (anything a program could not write today): a two-page
  RFC under `rfcs/` — the shape, its collision class (stock bash rejects it, or
  accepts it and needs an escape), how it lowers to plain Go, one fixture.
  Every admitted construct must lower to ordinary Go and must not re-spell
  something Go already has; that test decides most proposals. During alpha
  the maintainer is the sole approver and says so.
- **A fenced language**: one island file plus one real-repo example, as a
  bounded issue ("island of the month").

Discussions are on the language repo; there is no chat server until there are
people to fill one.
