# S154.3 — reading the audit (as of this commit)

`FINDINGS.md` is generated (`BASHPP_AUDIT_CORPUS=<go/test> go test ./gosource/testdata/sprint154/parser/`;
§Spike S is appended by the out-of-tree spike, see below). This file is the analyst's reading of it.

## Part 1 — first failing stage over the 83 roots

| stage | roots | wording | position | multiplicity | extra | missing |
|---|---|---|---|---|---|---|
| scanner | 3 | 3 | – | – | – | – |
| parser | 47 | 33 | 1 | 9 | 4 | – |
| checker | 16 | 6 | – | 6 | 3 | 1 |
| pass | 17 | | | | | |

- 50 of 83 roots fail first in the scanner/parser. 36 of those are pure wording (`expected ':', found newline` vs
  `unexpected newline, expected :`, …); 17 are wording-only in every failure. The 9 `multiplicity` and 4 `extra`
  parser rows are go/parser's error recovery emitting more diagnostics than gc (the `semi*.go` family: repeated
  `expected ';', found 'EOF'`), and the one `position` row (`syntax/topexpr.go`) is a recovery that reports on the
  wrong line.
- 37 roots carry `chk-after-syn`: the front end runs go/types after a parse failure. gc does not. In 7 checker-stage
  roots that is the *only* failure (bug014, bug068, bug169, bug300, bug388, issue23586, issue30722): the parser
  matched, the checker added Unmatched Errors.
- 7 checker-stage roots are label/branch checks (goto.go, label.go, label1.go, bug136, bug179, issue7538a,
  syntax/typesw.go): gc does these in its **parser** (`branches.go`, `CheckBranches`), go/types does them with
  its own wording (`declared and not used` vs `defined and not used`, `continue not in for statement` vs
  `continue is not in a loop`).
- 2 checker rows are neither: `fixedbugs/issue11362.go` (non-canonical import path — gc's noder checks it; nothing
  in the Bash++ front end does) and `import6.go` (bad import paths reach the importer as "could not import"
  instead of gc's `import path cannot be …`).
- go/types related-info parts (`\tprevious case`, `\tother declaration of x`) were folded into their main
  diagnostic for the audit (`related=n`), the way gc prints them. `gosource.ErrorList.Error()` prints each as a
  standalone `file:line:col: \t…` line, which the upstream harness would count as an Unmatched Error: 17 roots carry
  them, including **15 of the 17 roots that pass here** (bug132, bug200, bug412, bug416, issue15898, issue24159,
  issue28085, issue28268, issue33460, issue6977, mainsig, switch5, switch7, typeparam/issue48711, typeswitch2).
  Whether those pass in the leaf run depends on the `--check` printer, not on any parser — a one-line fold in the
  printer is the whole fix, and it is a precondition for the parser number below to show up in the leaf run.

## Part 2 — Spike S (gc `syntax` as the syntax verdict)

- gc's parser, driven with `CheckBranches` and gc's one-syntax-error-per-line filter, turns **49 of the 50**
  scanner/parser-stage roots to pass. Without the per-line filter it is 43 (six roots where gc's parser emits
  two syntax errors on one line; `base.ErrorfAt` drops the second).
- The remaining one is `fixedbugs/issue50372.go`: go/parser rejects `for a, b, c := range` in the parser, gc's
  parser accepts it and types2 reports it. This is the only syntax-side accept/reject divergence.
- On the checker side, gc's parser also closes the 7 label/branch roots (they pass under gc syntax alone) and
  rejects `syntax/typesw.go`'s bad guard in the parser. `fixedbugs/issue23586.go` is the reverse: go/parser
  rejects `defer` of a non-call in the parser, gc's parser accepts and types2 reports it.
- Package facts: 16 non-test files, 7533 lines, std-only imports (`fmt`, `go/build/constraint`, `go/constant`,
  `io`, `os`, `path/filepath`, `reflect`, `regexp`, `strconv`, `strings`, `unicode`, `unicode/utf8`); builds
  unmodified outside GOROOT.

## What the number says for D3

Of the 66 failing roots, a gc-wording parser closes 49 outright and 7 more via its branch checks (56);
a "no type-check after syntax errors" policy closes 7; the rest (issue50372, issue11362, import6.go,
issue23586) need small targeted checks. No row is a scanner-only problem in a way gc's scanner would not
also fix. Position/multiplicity rows all vanish under gc's parser + per-line filter — the go/parser
recovery differences are not separable from its wording.

## Reproducing Spike S

The scratch copy is not committed (`.git/info/exclude` → `scratch/`). To rerun: copy the Go 1.27.0
`cmd/compile/internal/syntax` non-test files to `scratch/gcsyntax/`, put a test alongside that calls
`audit.Classify` on `syntax.Parse(..., CheckBranches)` diagnostics (with the `base.ErrorfAt` per-line filter),
and run it with `BASHPP_AUDIT_CORPUS`, `BASHPP_AUDIT_ROOTS=audit-roots.txt`, `BASHPP_AUDIT_FINDINGS=<FINDINGS.md>`.
The spike driver source is kept next to the findings as `spike_s_test.go.txt` (a .txt so the committed package never imports the uncommitted scratch copy).
