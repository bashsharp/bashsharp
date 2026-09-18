# bashpp — Bash++ / Bash#

**Bash++ is a programming language for agents: the bash you already know,
Go where you need types, any fenced language where you need a library, and
`agentic` where you need a model — with contracts so a model's output is
judged, never trusted.**

Five clauses, in order of precedence, each with its gate:

1. **Base — GNU Bash 5.3.** A strict superset: every Bash 5.3 program is a
   Bash++ program with the same meaning. Gate: the 86-fixture suite, OFF
   86/86 and ON 79 + 7.
2. **Standard — POSIX.1-2016.** Through the same engine and the certified
   [coreutils](https://github.com/qiangli/coreutils); in `--posix`/cert mode
   the dialect is inert. Gate: the POSIX arms.
3. **Typed core — Go 1.27.1, mixed.** Go declarations, types, expressions,
   control flow, generics and the reviewed standard library inside shell
   text, interpreted and lowered to ordinary Go; whole Go programs via
   `--source=go`. Gate: the upstream Go 1.27.1 corpus (Barrier D by ID),
   Tour, Go by Example.
4. **Polyglot — fenced code blocks.** `~~~python`, `~~~typescript`,
   `~~~rust`, `~~~c`/`~~~cpp`, plus `~~~go` and `~~~bash`/`~~~sh` islands,
   exposed as ordinary callables with typed values crossing the boundary.
   Gate: the polyglot gates.
5. **Agentic — the word `agentic` is reserved for agentic features.** It is
   the language's `unsafe`: an `agentic { … }` body is delegated to a model
   and is the one place determinism moves from the translator to the judge;
   `@require`/`@ensure` contracts, decorators and advice are its
   deterministic guards. No other construct uses the word; no agentic
   feature is spelled without it. Gate: the agentic, decorator and contract
   suites.

The engine that parses and runs Bash stays in
[`sh`](https://github.com/qiangli/sh) (the certified Bash 5.3 interpreter);
this repo holds the language — today its docs, decisions and status, and
(Sprint 211) its evaluator, lowering compiler, Go-source front end,
polyglot islands and the `bashpp` binary:

- `docs/` — the language contract, design decisions, and the **Go corpus
  status pages** (`go-corpus-state-*.md`, `go-corpus-targets.{md,tsv}`),
  regenerated at every full barrier so one page always states where Bash++
  Go stands.
- Go packages: none yet. Sprint 207 measured the seam and left the dialect
  code in `sh` on purpose — see `CLAUDE.md` §What stays in sh and why.

## Companions

Bash++ is a language, not a userland. The commands a Bash++ program calls
come from its siblings, all pure Go, one identical toolset on Linux, macOS
and Windows:

- [`qiangli/coreutils`](https://github.com/qiangli/coreutils) — **the full,
  rounded set of Unix utilities**: the 116 POSIX-required names ∪ GNU
  coreutils (`ls`, `sed`, `awk`, `grep`, `find`, `sort`, `pax`, `make`, …),
  the certified POSIX package this language's clause 2 stands on.
- [`qiangli/sh`](https://github.com/qiangli/sh) — the Bash 5.3 engine
  (parser, expansion, interpreter) Bash++ extends.
- [`qiangli/yoke`](https://github.com/qiangli/yoke) — the agentic userland
  (`tar`, `jq`, `tree`, `git`, the fleet/kb/meet hub) and the managed
  toolchains the fenced languages run on.
- [`qiangli/bashy`](https://github.com/qiangli/bashy) — the shell that fronts
  all of the above (`bashy --bashpp`).
- [`qiangli/bashpp-tests`](https://github.com/qiangli/bashpp-tests) — the
  conformance gate.

Read `CLAUDE.md` before changing anything here.

## The goal, in one line

Every root of the upstream Go 1.27.1 test corpus passes every *applicable*
Bash++ mode (interpreted and compiled). The only admissible exclusions are
compiler artifacts, listed by ID with one of seven reasons — see
`docs/go-corpus-targets.md`.

## License

BSD-3-Clause, the same as `sh` (derived from `mvdan.cc/sh`).
