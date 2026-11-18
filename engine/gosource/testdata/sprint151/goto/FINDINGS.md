# Sprint 151 / Story #75 — goto and bare labels (S151.4 converter_goto cluster)

Follow-up to `../FINDINGS-M2.md` §1–2. After M2 (labeled break/continue via
`BashPPBranch.Depth`) the 38 roots in `../leaf/S151.4_converter_goto.roots`
still failed with `unsupported LabeledStmt` (label seen first: backward goto,
label on a non-loop) or `unsupported labeled branch` (branch seen first:
forward goto). This directory holds the out-of-corpus reproducers; each row of
`TestSprint151Implemented` in `sh/gosource/sprint151_spike_test.go` runs one
through the interpreter AND through `lower` + `go run` and diffs both against
real Go.

## Mechanism

Two nodes in `sh/syntax/bashpp_nodes.go`, produced only by the Go-source
converter (the Bash++ parser has no label syntax):

- `BashPPLabeled{Label, Colon, Stmt}` — Go's `LabeledStmt`. `Stmt == nil` is
  the empty statement (a label at the end of a block). A labeled `for`,
  `range`, `switch`, type switch, or `select` still resolves `break L` /
  `continue L` through `BashPPBranch.Depth` (M2); the label is carried for
  `goto` and for lowering. A label on any other statement is a goto target
  only — a loop nested inside it must not take the label, so the converter
  clears `statementLabel` before converting the inner statement.
- `BashPPGoto{Kw, Label}`.

Interpreter (`sh/interp`): `goto` raises `bashPPBranchGoto` with the label
name and unwinds exactly like `break` — every loop/switch/select/block
returns — until `Runner.stmts` finds a `BashPPLabeled` with that label in the
statement list it is executing, then that list resumes at the label's index.
Go forbids jumping into a block, over a declaration, or across a function, and
go/types has already rejected those programs, so the label is always in the
current block or an enclosing one and the runner validates nothing.

The one subtlety is scoping on a backward goto: `L: x := f(); …; goto L`
re-runs the short declaration, which in Go creates a *new* variable each pass
(closures created on earlier passes keep their own `x`). `BashPPLabeled`
therefore records, in the block's scope, the names declared when the label was
reached (`bashPPScope.markLabel`); resuming at the label drops every name the
block declared after that point (`resumeAtLabel`), so the declaration runs
afresh and `BASHPP-ESHORT-NONEW` is not raised. `goto_backward_loop.go` pins
the per-pass closure capture.

Lowering (`sh/lower`): labels and `goto` are emitted verbatim. A labeled
loop/switch/select seeds its branch target with the source label
(`emitter.pendingLabel`, consumed by `pushBranchTarget`) so that a
depth-resolved `break L` names the same label instead of a synthesized
`__branchN`, and — because Go rejects an unused label — a depth-1 labeled
branch keeps its label when the innermost target carries one
(`fixedbugs/issue49145.go`, `bug137.go`, `inline.go` were the corpus roots
that caught this). Function literals clear `pendingLabel`: labels are
function-scoped.

## Reproducers (all `go vet` clean, deterministic)

| file | shape |
|---|---|
| `../goto_forward.go` | forward goto, branch precedes label |
| `../goto_backward.go` | backward goto loop, label precedes branch |
| `goto_backward_loop.go` | backward goto over a short declaration; closures keep per-pass cells |
| `goto_out_of_nested.go` | goto out of `range` → `if` → `range` to a label in the function body |
| `goto_over_switch.go` | forward goto skipping a switch; goto from a switch arm; goto from a select arm (both directions) |
| `goto_label_at_end.go` | label on the empty statement at block end; label on a block; `_:` (the only label Go lets go unused) |
| `goto_labeled_loop.go` | one label used by `continue L`/`break L` and by `goto`; labeled switch broken from a nested loop (depth 2) and directly (depth 1) |

## Measurement on the 38 roots

Probe modes mirror `bashpp-tests/tools/go-full/product.rb`: interpreted =
`bashy --bashpp --source=go --check <root>`, compiled = `bashy transpile
--bashpp --source=go <root> -o generated.go` followed by `go build` of the
output; `run` roots were additionally executed and their stdout + exit code
diffed against `go run`. (Binary: bashy built with `-modfile` pointing
`mvdan.cc/sh/v3` at this workspace; the shared bashy checkout was not edited.)

- **35/38 single-file roots convert and check clean** in interpreted mode; the
  8 `run` roots (`bug005`, `bug178`, `issue13684`, `issue40367`, `issue4748`,
  `issue49145`, `issue75569`, `ken/label.go`) produce byte-identical stdout and
  exit code to `go run`.
- **33/38 lower to Go that `go build`s.** The residue:
  - `escape2.go`, `escape2n.go` (compiled mode only) — body-less declarations
    (`func external(*int)`). Lowering used to panic on the nil body; it now
    reports `LOWER-EUNSUPPORTED: function declaration without body`. A
    body-less declaration needs assembly or a linkname, so the generated Go
    could not build regardless; these are `errorcheck -m` roots whose value is
    the diagnostic, and their interpreted probe is clean.
  - `issue15838.go`, `issue19699.go`, `issue7023.go` — `compiledir` roots whose
    `.dir` holds two packages (`a` + `b`/`main`, `b` importing `./a`). Passing
    the directory to bashy stops at `found packages a (a.go) and b (b.go)`;
    the ledger harness instead compiles the packages in order
    (`inventory.py: compile-packages-in-order`), which this workspace cannot
    reproduce. The ledger's failure for all three was in `a.go` (the package
    that carries the goto), and each `a.go` now converts and lowers cleanly on
    its own; `b.go`'s `./a` relative import is the S151.1 package-map
    mechanism, not this cluster.

## What remains

- Labels are matched by name within a function, which is exact for Go; the
  `(label, scope)` bookkeeping lives in the block scope, so a label reached in
  one activation of a recursive function cannot be confused with another.
- `lower` places a labeled `for` whose init expands to a compound checked
  binding inside a synthesized `{ init; L: for … }` block. Go source never
  produces such an init, so no root exercises it; a goto to that label from
  outside the synthesized block would be rejected by Go.
- Appending closures to a `[]func() int` (`BASHPP-ECOLLECTION-ELEMENT: scalar
  cannot be used as func()(int)`) is a pre-existing collection-element gap
  found while writing `goto_backward_loop.go`; the reproducer was reshaped to
  avoid it. Not a goto mechanism.
