# gcsyntax — vendored gc parser (the Bash++ syntax verdict)

This package is a verbatim copy of the Go compiler's own parser:

- Upstream: **go1.27.0** `src/cmd/compile/internal/syntax`
- License: BSD-3-Clause (see `LICENSE` in this directory; per-file Go
  copyright headers are kept unmodified)
- Contents: the 16 non-test Go files of the upstream package. The
  upstream `*_test.go` files and `testdata/` directory are dropped.

The files are **unmodified except the package import path** — the
package is imported as `mvdan.cc/sh/v3/gosource/internal/gcsyntax`
instead of `cmd/compile/internal/syntax`. The import path is implied by
the directory location; no file content was edited (all upstream
imports are standard library, so the package compiles outside GOROOT
as-is).

## Why it is vendored

gosource (the Bash++ Go-source front end) parses with `go/parser`, but
gc parses with this package, and the two disagree on error wording,
positions and multiplicity. Sprint 154's parser-exclusive causal audit
(`gosource/testdata/sprint154/parser/FINDINGS.md`, §Spike S) measured
that driving this package as
`syntax.Parse(NewFileBase(name), src, errh, nil, syntax.CheckBranches)`
turns 49 of the 50 parser/scanner-stage corpus failures into passes.

gosource therefore runs this parser FIRST on every source file: if it
reports syntax errors, gosource emits exactly those errors (gc wording,
positions and multiplicity, including the CheckBranches label/goto/
break/continue errors gc reports at syntax stage) and stops — gc runs
no type checker after a syntax error. If it accepts, gosource continues
with `go/parser` + `go/types` exactly as before. See
`gosource/verdict.go`.
