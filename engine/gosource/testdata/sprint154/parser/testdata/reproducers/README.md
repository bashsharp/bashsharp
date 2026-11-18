# S154.3 reproducers — out-of-corpus, one directory per row family

Each directory holds `reject.go.src` (a 5-line form the Bash++ front end
diagnoses) and `accept.go` (the nearest accepted form). None is a corpus
copy. `reproducers_test.go` only checks accept/reject — the wording below is
the finding, never a gate. gc output is from the Spike S scratch build of
`cmd/compile/internal/syntax` (Go 1.27.0) with `CheckBranches`.

Since S154.1 the gc column IS what the front end emits: gosource parses
with the vendored gc parser first (`gosource/internal/gcsyntax`), and a
gc rejection is the complete diagnostic set. `gosource/verdict_test.go`
(`TestSyntaxVerdictReproducers`) asserts the exact gc line for every
`reject.go.src` below — for `parser-range-three`, where gc's parser accepts,
the types2 diagnostics gc prints (`gosource/range_arity.go`, Sprint 270). The "Bash++ (go/parser)"
columns below are the S154.3 pre-verdict findings, kept for the record.

## Parser rows (three)

| dir | corpus row | Bash++ (go/parser) | gc (syntax) |
|---|---|---|---|
| `parser-else` | `syntax/else.go` (parser/wording) | `5:9: expected if statement or block, found x` | `5:9: syntax error: else must be followed by if or statement block` (+ one more on the same line, dropped by gc's one-syntax-error-per-line filter) |
| `parser-case-colon` | `switch2.go`, `fixedbugs/issue18092.go` (parser/wording) | `5:8: expected ':', found newline` | `5:8: syntax error: unexpected newline, expected :` |
| `parser-range-three` | `fixedbugs/issue50372.go` (parser/wording; the one accept/reject divergence on the syntax side) | `4:12: expected at most 2 expressions` | gc's parser **accepts**; the corpus message `range clause permits at most two iteration variables` comes from types2 |

Same position in every case; only the wording differs, which is the Part 1
result in miniature (33 of the 47 parser-stage roots are `wording`, and gc's
parser closes 49 of the 50 scanner/parser-stage roots — see `../FINDINGS.md`).

## Checker rows (two)

| dir | corpus row | Bash++ | gc |
|---|---|---|---|
| `checker-label-unused` | `label.go`, `label1.go`, `goto.go`, `fixedbugs/issue7538a.go` (checker/wording) | `4:1: label L declared and not used` (go/types) | `4:1: label L defined and not used` — from gc's **parser** (`branches.go`, `CheckBranches`), not its checker |
| `checker-after-syntax` | `fixedbugs/bug014.go`, `bug068.go`, `bug169.go`, `bug300.go`, `bug388.go`, `issue23586.go`, `issue30722.go` (checker/multiplicity, `chk-after-syn`) | `3:14: illegal character U+0027 ''' in escape sequence` (scanner) **and** `3:11: malformed constant: '\0'` (go/types) | `3:14: invalid character '\'' in octal escape` only — gc never type-checks a file with syntax errors |

The second family is a front-end policy, not a parser difference: the
Bash++ front end runs go/types over a file that failed to parse, and every
diagnostic it adds is an Unmatched Error under the upstream harness.

`reject.go.src` files are deliberately invalid Go and carry the `.src` suffix so
the repository-wide gofmt gate (`scripts/fmtcheck.sh`, which parses every
tracked `*.go`) does not reject the tree; the tests load them under the name
`reject.go`.
