# Sprint 152 triage

Commands below used the isolated `bashy.real` built from the sibling
`bashy` checkout with this workspace substituted only for `mvdan.cc/sh/v3`.
For lowered programs, `go run` was executed in a scratch module with
`replace mvdan.cc/sh/v3 => <this workspace>`.

## `testdir:fixedbugs/bug367.go` (interpreted `panic: should not satisfy main.I`)

Reproducer: [`reproducers/bug367/main.go`](reproducers/bug367/main.go), with
the separate package-private method in `reproducers/bug367/p/p.go`.

* Oracle: `go run .` exits 0.
* Interpreter: exits 2; the reduced case reports `panic: should satisfy p.I`
  (the corpus variant instead accepts the foreign private method and reports
  `panic: should not satisfy main.I`).
* Lowering: transpile exits 0; a standalone run of its output cannot resolve
  the reproducer's module import, so no lowerer comparison is available.

Mechanism: private interface methods are package-identity-sensitive, while the
interpreter bridge conflates or loses that identity across the package boundary.

Verdict: **153 (runtime: package-private interface assertion bridge)**.

## `testdir:fixedbugs/issue4167.go` (interpreted `exit status 2`)

Reproducer: [`reproducers/issue4167/main.go`](reproducers/issue4167/main.go).

* Oracle: `go run main.go` exits 0.
* Interpreter: exits 2 with no diagnostic.
* Lowered Go: transpile exits 0 and `go run .` exits 0.

Mechanism: a method expression supplied from a multi-result call is accepted
by Go and by lowered Go but terminates in the interpreter.

Verdict: **153 (runtime: interpreter call/result bridge)**.

## `testdir:for.go` (interpreted `assertion fail incorrect index value after range loop`)

Reproducer: [`reproducers/for/main.go`](reproducers/for/main.go).

* Oracle: `go run main.go` exits 0.
* Interpreter: exits 0 in this checkout (the manifest-era assertion no longer
  reproduces).
* Lowered Go: transpile exits 0 and `go run .` exits 0.

Mechanism: the only reported row is interpreted and targets the final index of
a range loop; no generated-Go diagnostic is involved.

Verdict: **153 (runtime: range-loop final-index comparison; currently fixed locally)**.

## `testdir:reorder.go` (interpreted `[1 100 3], want 100,2,3` and `panic: failed`)

Reproducer: [`reproducers/reorder/main.go`](reproducers/reorder/main.go).

* Oracle: `go run main.go` exits 0.
* Interpreter: prints `[1 100 3], want 100,2,3`, then `panic: failed` and
  exits 2.
* Lowered Go: transpile exits 0; `go run .` prints the same line and panic
  and exits 1.

Mechanism: multi-assignment evaluates the destination index after assigning
the preceding left-hand side, rather than evaluating all assignment operands
before writes.

Verdict: **153 (runtime: assignment evaluation order)**. The separate compiled
manifest row (`LOWER-ETYPE` at the full source's multi-result assignment) is a
different 152 lowering row and is not reclassified by this interpreted-row
decision.

## `testdir:fixedbugs/issue15091.go` (compiled race linker diagnostic)

Reproducer: [`reproducers/race/main.go`](reproducers/race/main.go); the
upstream root was also compiled with its `errorcheck -0 -race` directive.

* Oracle: `go tool compile -race -e issue15091.go` exits 0 (the source is an
  errorcheck package, not a runnable `main` program); `go run -race` of the
  reproducer exits 0.
* Interpreter: the runnable reproducer exits 0.
* Lowered Go: the lowered upstream source passes both `go test .` and
  `go test -race .`; lowered reproducer `go run -race .` exits 0.

Mechanism: race instrumentation introduces `runtime.racefuncenter`; it links
only when the final link also receives `-race`, whereas `errorcheck -0` asks
for compilation only.

Verdict: **harness (the errorcheck recipe linked a `-race` object without the
race runtime; retain the compile-only contract or propagate `-race` to link)**.

## `testdir:fixedbugs/issue17449.go` (compiled race linker diagnostic)

Reproducer: [`reproducers/race/main.go`](reproducers/race/main.go); the
upstream root was also compiled with its `errorcheck -0 -race` directive.

* Oracle: `go tool compile -race -e issue17449.go` exits 0; `go run -race` of
  the reproducer exits 0.
* Interpreter: the runnable reproducer exits 0.
* Lowered Go: the lowered upstream source passes both `go test .` and
  `go test -race .`; lowered reproducer `go run -race .` exits 0.

Mechanism: this is the same missing-final-link-flag condition as issue15091,
not a diagnostic emitted by the lowerer.

Verdict: **harness (the `errorcheck -0 -race` recipe must not perform an
unpaired link)**.

## `testdir:alias3.go` (compiled `LOWER-EUNDEFINED: undefined: IntAlias`)

Reproducer: [`reproducers/alias3/a/a.go`](reproducers/alias3/a/a.go), consumed
through `reproducers/alias3/b/b.go` by `reproducers/alias3/main.go`.

* Oracle: `go run .` exits 0.
* Interpreter: exits 0.
* Lowered Go: the current isolated build transpiles the alias package and
  `go test .` of that output exits 0; thus the recorded manifest diagnostic
  does not reproduce at this revision.

Mechanism: the manifest's `LOWER-EUNDEFINED` is emitted while lowering the
package-map alias declaration (`alias3.dir/a.go:14`), before program execution.

Verdict: **152 (lowering: package-map alias declaration emission in
`alias3.dir/a.go`)**.
