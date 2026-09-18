# bashpp — Bash++ / Bash#

Bash++ is the dialect layer over the [bashy](https://github.com/qiangli/bashy)
shell: a strict superset of GNU Bash 5.3 that admits Go 1.27 syntax and
semantics inside ordinary shell scripts, plus the Bash# ergonomics tier. The
engine that parses and runs Bash stays in [`sh`](https://github.com/qiangli/sh)
(the certified Bash 5.3 interpreter); this repo holds everything that is
*dialect*, not *engine*:

- `docs/` — the language contract, design decisions, and the **Go corpus
  status pages** (`go-corpus-state-*.md`, `go-corpus-targets.{md,tsv}`),
  regenerated at every full barrier so one page always states where Bash++
  Go stands.
- Go packages (arriving with Sprint 207): the lowering compiler (`lower`), the
  Go-source front end (`gosource`), and the typed JSON codec (`syntax/typedjson`).

The conformance gate is [`bashpp-tests`](https://github.com/qiangli/bashpp-tests).
Read `CLAUDE.md` before changing anything here.

## The goal, in one line

Every root of the upstream Go 1.27.1 test corpus passes every *applicable*
Bash++ mode (interpreted and compiled). The only admissible exclusions are
compiler artifacts, listed by ID with one of seven reasons — see
`docs/go-corpus-targets.md`.

## License

BSD-3-Clause, the same as `sh` (derived from `mvdan.cc/sh`).
