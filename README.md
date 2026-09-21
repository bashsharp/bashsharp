# Bash#

**Bash#** ("bash sharp") is a programming language for agents: *the bash you
already know, Go where you need types, any fenced language where you need a
library, and `agentic` where you need a model — with contracts so a model's
output is judged, never trusted.*

It runs inside [bashy](https://github.com/qiangli/bashy), a pure-Go Bash 5.3
for Linux, macOS and Windows. This repo is the language: its definition, its
front door (`front/`, `transpile/`, `cmd/bashsharp`), its design decisions,
and the status pages that say exactly what is measured. The engine that parses
and runs it lives in [qiangli/sh](https://github.com/qiangli/sh); the tests
that decide every claim live in
[bashsharp/bashsharp-tests](https://github.com/bashsharp/bashsharp-tests).

> **Alpha.** Syntax may change before 1.0. The way to change it is a two-page
> RFC (see [ROADMAP.md](ROADMAP.md)); the way to help is the conformance suite,
> which is red on purpose where the language is not finished.

## Ten minutes

Install bashy (one static binary; [releases](https://github.com/qiangli/bashy/releases/latest)),
then take the tour — 27 small programs, each with its transcript, and one
script that runs them all on your machine. It also comes as a procedure your
coding agent can drive.

Deciding whether to use it at all: [docs/why-bashsharp.md](docs/why-bashsharp.md).

**→ [bashsharp/tour](https://github.com/bashsharp/tour)**

The shortest possible version:

```bash
@guard(effects: "read")
@require('test -n "$1"')
@ensure('test "$1" != lie')
agentic function summarize() { ... }

agentic {
    summarize ok        # 0
    summarize ""        # 3 — precondition failed; the body never ran
    summarize lie       # 3 — the body said fine; the postcondition disagreed
    summarize yield     # 6 — "I need input": a yield, not a made-up answer
}
```

The interpreter never calls a model. `agentic` marks the one place a program
may hand work to one; it can only be *called* from inside an explicit
`agentic { }` scope; the contracts around it are ordinary shell commands, run
deterministically; and status 6 is a defined *yield* that reaches whoever ran
the script.

## What it is, in five clauses

Each clause names its gate; the numbers are in [docs/claims.md](docs/claims.md).

1. **Base — GNU Bash 5.3, a strict superset.** Every Bash 5.3 program is a
   Bash# program with the same meaning; with the flag off the dialect does not
   exist. *Gate: Bash's own 86-fixture suite, dialect off 86/86, on 79 + 7.*
2. **Standard — POSIX.1-2016.** Through the same engine and the pure-Go
   [coreutils](https://github.com/qiangli/coreutils); under `--posix` the
   dialect is inert. *Gate: the licensed VSC shell arm, yash's POSIX suite.*
3. **Typed core — Go 1.27, mixed.** Go declarations, typed functions, calls
   written as words, imports, control flow inside bodies; whole Go programs
   (generics and all) via `--source=go`; every construct lowers to ordinary
   Go. *Gate: the upstream Go 1.27.1 test corpus, the Go Tour, Go by Example.*
4. **Polyglot — fenced islands.** `~~~python`, `~~~typescript`, `~~~rust`,
   `~~~c`/`~~~cpp`, `~~~go`, `~~~bash`/`~~~sh` blocks become ordinary
   callables with typed values crossing the boundary. A fence never
   resolves its tool from `PATH`: under bashy each island's toolchain is
   provisioned — pinned, checksum-verified, cached — so the same program
   means the same thing on every machine; `BASHPP_*` names a program
   explicitly. *Gate: the polyglot suites; the tour's stripped-PATH leg.*
5. **Agentic — the reserved word.** `agentic` is the language's `unsafe`; the
   `@require`/`@ensure`/`@guard` contracts, decorators and advice are its
   deterministic guards. No other construct uses the word. *Gate: the
   agentic, decorator and contract suites.*

And a sixth thing that is not a clause but what shell people ask for first —
the **Sharp** tier: decorators (`@name(args)`, a function taking `c *Call`),
keyword and default arguments, exhaustive enums, deep `readonly`, and a
null-safety **check** (`bashy check`). Admitted only when it lowers to plain
Go and collides with nothing bash already accepts.

## Using it

- `bashy --bashsharp file.bsh` (or `#!/usr/bin/env -S bashy --bashsharp`); a
  `.bsh` extension turns it on by itself. `--no-bashsharp` and `--posix` turn
  it off.
- `bashy --bashsharp --source=go program.go` runs a whole Go program.
- `bashy transpile --bashsharp file.bsh -o file.go` lowers a file to Go.
- `bashy check --bashsharp file.bsh` runs the static checks (null safety).
- `bashsharp` (this repo's `cmd/bashsharp`) is the same front door over the
  engine alone — what the conformance harness measures.

Add `--standalone` to either transpile front door to write a `go.mod` beside
the `-o` output. The module pins the forked shell runtime used by generated Go,
so `go build -mod=mod .` works without a sibling checkout; it requires Go 1.27
or newer. Standalone mode requires `-o` and refuses an existing `go.mod`
unless `--force` is also given. See [docs/transpile.md](docs/transpile.md).

Every extension runs as Bash# in bashy — `.sh`, `.bash`, `.bpp`, `.bsh`; the
name never gates the content. `.bsh` is the official one; `.bpp` is an
accepted alias (the middle rung of bash → bash++ → bash#), as are the
Bash++-era flag and variable (`--bashpp`, `BASHY_BASHPP`) — aliases, not
deprecations: they resolve identically, print nothing and have no expiry;
only the Bash# spellings are promoted. The language was called Bash++
until 2026-09-18: [rail5/bashpp](https://github.com/rail5/bashpp) has that
name, and had it first — [docs/naming-collision.md](docs/naming-collision.md).

## What's here

| path | what |
|---|---|
| [docs/why-bashsharp.md](docs/why-bashsharp.md) | why Bash#, and for what — the page to hand to whoever has to approve it: four properties, ranked use cases, deployment shapes, honest comparisons, what is planned |
| [docs/claims.md](docs/claims.md) | every number and its corpus; what is not claimed |
| [ROADMAP.md](ROADMAP.md) | the five clauses by stage, the open design calls, the RFC process |
| [docs/go-corpus-state-2026-09-17.md](docs/go-corpus-state-2026-09-17.md) · [docs/go-corpus-targets.md](docs/go-corpus-targets.md) | the Go-corpus status and every failing root by class |
| [docs/bashpp-posix-superset-syntax.md](docs/bashpp-posix-superset-syntax.md) | the syntax contract: which shapes are admitted and why they are safe (the collision map) |
| [docs/bashsharp-ergonomics-tier.md](docs/bashsharp-ergonomics-tier.md) · [docs/bashpp-decorators-and-advice.md](docs/bashpp-decorators-and-advice.md) | the Sharp tier and decorators/advice |
| [docs/bashpp-agentic-mvp-plan.md](docs/bashpp-agentic-mvp-plan.md) | the `agentic` contract |
| [docs/transpile.md](docs/transpile.md) | generated Go, source maps and standalone module output |
| `front/` · `transpile/` · `cmd/bashsharp/` | the dialect selector, the Go-source interface, the lowering entry point, the binary |

Design notes under `docs/` keep their original file names (many say `bashpp`)
and their engineering register — they are the decisions of record, not
tutorials. Start with the tour.

## Companions

All pure Go, one identical toolset on Linux, macOS and Windows:

- [qiangli/bashy](https://github.com/qiangli/bashy) — the shell you install; Bash# is what `bashy --bashsharp` speaks.
- [qiangli/sh](https://github.com/qiangli/sh) — the Bash 5.3 engine, a fork of [mvdan/sh](https://github.com/mvdan/sh).
- [qiangli/coreutils](https://github.com/qiangli/coreutils) — the POSIX-required and GNU coreutils applets.
- [qiangli/yoke](https://github.com/qiangli/yoke) — the agentic userland (`git`, `jq`, `tar`, the fleet/kb/meet hub, managed toolchains).
- [bashsharp/bashsharp-tests](https://github.com/bashsharp/bashsharp-tests) — the conformance gate.
- [bashsharp/tour](https://github.com/bashsharp/tour) — getting started.

## License

BSD-3-Clause, the same as `sh` (derived from `mvdan.cc/sh`). See [NOTICE](NOTICE).
