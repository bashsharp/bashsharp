# Sprint 165 — lane `gosource-diag` (S165.5, story #101) findings

Measured on this lane's candidate (sh: this branch; bashy at the Barrier C
pin) with the exact upstream Go 1.27 check_test runners patched as the
harness patches them (`testdata/types-backend/*/check_test.go.patch`), all
371 go/types + 372 types2 fixtures; testdir roots by the check-then-run
argv of the backend hook. Run 0 (`leaf-165r0/`, the leaf host) was read
before the second cluster; its typechecker and unclassified rows are the
Barrier C rows minus `bug130` (PASS at run 0 — the only PASS of 529).

Status vocabulary: `fixed in <sha>` · `harness` (first cause in
bashpp-tests; diff under "Requests") · `design` (needs the front end of
`docs/bashpp-source-preserving-front-end.md`; recommend by ID) · `moved to
<owner>` (first cause outside this seam) · `by-ID D<n>`.

## 1. The typechecker cluster (18 roots)

First causes, in the order they were found:

- **(a) harness continuation fold** — bashpp-tests `541f27c` made the
  go/types runner hook drop every TAB-prefixed line of the check output.
  go/types joins a sub-error that has no position into the primary's own
  `Msg` (`errors.go report`: `"not enough arguments in call to f1\n\thave
  ()\n\twant (int)"`, `"impossible type assertion: t.(T)\n\tT does not
  implement I (...)"`) and the fixtures' `ERROR` comments match against
  the joined message; only a *positioned* secondary (`\t<file>:<l>:<c>:
  <msg>`) is the separate Error upstream ignores. Before `541f27c` the hook
  folded unpositioned lines (the `m == nil` path); after it, 11 go/types
  roots of the cluster fail with `no error expected: "<first line only>"`
  (9 on this alone; issues0.go and expr3.go also carry (b)/(e)). Product output
  is right (`ErrorList.Error()` prints the joined message); the runner's
  reconstruction is lossy. Diff under "Requests".
- **(b) repeated parser diagnostic** — the checker-test policy checked
  go/parser's *unmirrored* tree, so gc's parser row and go/types' row for
  one construct both reached the runner (method with no/several receivers,
  `...T` receiver, malformed literal). Fixed: the mirror applies under both
  policies, plus the `...T` receiver rule (`87da0fac`).
- **(c) filtered parser stream** — gc's per-line stderr filter was applied
  under the checker-test policy; the runners' parsers stream every error.
  Fixed (`ccf98f51`).
- **(d) forms go/ast cannot carry / recovery after a syntax error** — the
  front-end class; design note §1.1–§1.2.
- **(e) go/parser's columns at tolerance 0** — the go/types runner's parser
  is go/parser; the syntax verdict is gc's (Sprint 154 D3); design note
  §1.3.

| root | first cause | mechanism | status (both modes) |
|---|---|---|---|
| typechecker:cmd/compile/internal/types2/TestCheck/decls2 | (b): "method has no/multiple receivers" repeated by go/types at the receiver list after gc's parser reported it at the name | mirror under the checker-test policy | fixed in 87da0fac — PASS on the patched runner |
| typechecker:cmd/compile/internal/types2/TestCheck/issues0.go | (b): `func (... TT) f()` — gc's parser "invalid use of ..." then rewrites the receiver to `TT`; go/parser keeps the Ellipsis (a final `...T` is legal in a parameter list) and go/types adds "invalid syntax tree: invalid use of ..." | mirror: receiver `...T` → `T` as gc's paramList | fixed in 87da0fac — PASS |
| typechecker:go/types/TestCheck/issues0.go | (a) for the `\t\thave foo()` rows + (b) for 330:7 | harness fold + mirror | fixed in 87da0fac; PASS **only with the harness fold** |
| typechecker:go/types/TestFixedbugs/issue39634.go | (a): `too many arguments in call to F20\n\thave (unknown type)\n\twant ()` | harness fold | harness |
| typechecker:go/types/TestFixedbugs/issue49005.go | (a): `impossible type assertion: F2().(*X2)\n\t*X2 does not implement I (...)` | harness fold | harness |
| typechecker:go/types/TestFixedbugs/issue50816.go | (a) | harness fold | harness |
| typechecker:go/types/TestFixedbugs/issue54942.go | (a): `... (wrong type for method m)\n\t\thave m[P any](P) P\n\t\twant m(int) int` | harness fold | harness |
| typechecker:go/types/TestFixedbugs/issue58742.go | (a): `not enough return values\n\thave ()\n\twant (unknown type)` | harness fold | harness |
| typechecker:go/types/TestFixedbugs/issue70150.go | (a): `ERROR "have ([]int...)\n\twant (string, ...int)"` | harness fold | harness |
| typechecker:go/types/TestFixedbugs/issue70526.go | (a) | harness fold | harness |
| typechecker:go/types/TestFixedbugs/issue76103.go | (a) | harness fold | harness |
| typechecker:go/types/TestSpec/methods.go | (a): `(wrong type for method m)\n\t\thave m[P any](P) P\n\t\twant m(int) int` | harness fold | harness |
| typechecker:cmd/compile/internal/types2/TestCheck/stmt0.go | (d): `go 1;` / `defer 1;` — "must be function call" is types2's on gc's tree; go/parser sets `Call = nil`, go/types is silent (2 rows missing). Also (b)-shaped duplicate at 803: gc's parser "syntax error: cannot declare in post statement of for loop" at the `:=` and go/types "cannot declare in post statement" at the statement — types2 skips its copy ("The parser already reported an error"), go/types has no such switch; not fixed here (see §5) | front end | design (note §1.1) |
| typechecker:cmd/compile/internal/types2/TestLocal/issue47996.go | (c) fixed the six syntax rows (they now match at tolerance 0); (d) remains: go/parser's recovery of `func T [P] m () {}` leaves `P` referenced (`undefined: P`), gc's does not | parser stream + front end | (c) fixed in ccf98f51; design (note §1.2) |
| typechecker:cmd/compile/internal/types2/TestLocal/issue68183.go | (d): gc's scanner keeps `☹` inside the identifier (`atIdentChar`), types2's `isValidName` suppresses follow-ons; go/scanner splits ILLEGAL + `x` → `undefined: x`, `undefined: T`, `"fmt" imported and not used`, and the expected `undefined: _世界` is lost with the declaration | front end | design (note §1.2) |
| typechecker:go/types/TestCheck/decls2 | (e): gc's parser reports at the name (col 47/61/64/60/63), go/types at the receiver list (6/11/15/10/13); tolerance 0. After 87da0fac these are the only rows | — | design (note §1.3; recommend by ID) |
| typechecker:go/types/TestCheck/expr3.go | (a) for the have/want rows; (e) for `middle/final index required in 3-index slice` (gc at the colon that follows, go/parser at the colon that precedes) | harness fold; — | harness for (a); design for (e) |
| typechecker:go/types/TestCheck/stmt0.go | (d) the two "must be function call" rows + (e) `803:53 got col = 53; want 22` + the post-statement duplicate | front end; — | design |

Measured on the patched runners with the fold applied: go/types **367/371**
(decls2, expr3.go, stmt0.go fail — (e)/(d) only), types2 **368/372**
(stmt0.go, issue47996.go, issue68183.go — (d) only); every other fixture
that passed before still passes (verdict diff of the full suites: additions
only). With the *unmodified* hook, all twelve go/types roots of the cluster
still fail on (a) — the product commits alone flip only the two types2
roots. The leaf request below therefore needs the harness change to show
the go/types roots PASS.

## 2. nul1.go (154)

Barrier C / run 0 verdict `missing=0;wording=0;extra=1;class=multiplicity`;
errorCheck's output (run 0 evidence) shows the extra row is
`tmp__.go:25:27: undefined: A` — not a repeated NUL row. First cause: gc's
source layer drops NUL, invalid-UTF-8 and BOM bytes before its scanner sees
them (source.go `nextch`, positions still count them); go/scanner keeps
them as ILLEGAL tokens, and after `var \xc2A int` go/parser's recovery lost
the declaration, so go/types reported `A` undefined. Fixed in `833107e4`:
Load parses the image gc's scanner sees, positioned in the original file,
with each identifier's/literal's text restored from gc's raw segment
(`z\xc1\x81w` stays one name). nul1's generated program now loads line for
line as `go tool compile -e` (12 rows). A token's segment runs to the
character that ends it, as gc's does, so bytes dropped right after an
identifier or number stay in its text (`a2837eb8`: `var q\xff int` declares
`q\xff`; `var x = 1<BOM>` is gc's malformed constant — the Sprint 154
bom.go expectation recorded the syntax-stage row alone and is corrected to
gc 1.27's two rows). Exercised by `TestSprint165ScannerImage`; the
errorcheckoutput root itself is the leaf's to confirm (the bridge codec fix
of Sprint 162 already delivers the bytes).

## 3. The unclassified set (32 roots at C; 31 at run 0)

Owners confirmed by reading each row's first line to its reporting site.
Every `gosource:`-prefixed first line except `blank.go`'s is emitted by
`sh/interp` (the prefix names the source language, not the package):
`bashpp_concurrency.go`, `gosource_methods.go`, `gosource_complex.go`,
`bashpp_native_values.go`, `bashpp_native_bridge.go`,
`gosource_native_channels.go`. Corrections to the Sprint 162 ledger are
marked **(corrected)**.

| root | mode | first cause | owner |
|---|---|---|---|
| testdir:abi/method_wrapper.go | interpreted | `assignment mismatch: 5 variable(s) but 1 value(s)` (interp/bashpp_p1.go, bashpp_readonly.go): a multi-result call through a method wrapper delivers one cell | 151 |
| testdir:blank.go | both | first line was gosource's flat-namespace guard rejecting the second `func (T) _()` — **fixed in b2ec5b41**; next first cause is the interpreter's: `BASHPP-ESTRUCT-FIELD-DUPLICATE: field "_" declared more than once` for `_, _, _ int` (151) and the native bridge source emitting `_` as a value (`cannot use _ as value or type` in the generated session file, 153) | 151 + 153 **(corrected: was lower)** |
| testdir:chan/powser1.go | interpreted | `mixed native/interpreted channel select requires atomic arbitration` (interp/bashpp_concurrency.go:1484) — a select over a native and an interpreter channel | 153 |
| testdir:chan/select3.go | interpreted | `case x, ok := (<-closedch):` — parenthesized receive lowered without its receive (gosource/convert.go) — **fixed in d34872f1**; root runs to exit 0 on the candidate | gosource, fixed **(corrected: was runtime)** |
| testdir:fixedbugs/bug120.go | interpreted | `string for float64` — float rendering/conversion in the evaluator | 151 (Classic-parity case required) |
| testdir:fixedbugs/bug130.go | interpreted | `incomplete native selection reply` (interp/gosource_native_channels.go:79) at C; **PASS at run 0** — a bridge reply race, not deterministic | 153 if it recurs; otherwise closed by run 0 |
| testdir:fixedbugs/issue15002.go | interpreted | `index out of range [1] with length 1` — collection bounds | 151 |
| testdir:fixedbugs/issue30606b.go | interpreted | `string not assignable to reflect.Type` — reflect bridge type | 153 |
| testdir:fixedbugs/issue30862.go | both | `runindir -goexperiment fieldtrack`, `//go:nointerface` in `a/a.go`: the pragma changes method-set semantics under a GOEXPERIMENT; both modes report `test failed: fail 1` | 152/lower (pragma on the generated package; compiled) and 151 (interpreted has no `nointerface`) — likely by ID: a GOEXPERIMENT-gated compiler pragma |
| testdir:fixedbugs/issue50672.go | interpreted | `cannot resolve method f on func(int,int)(int)` (interp/gosource_methods.go:66): method resolution on a function-typed generic value | 151 |
| testdir:fixedbugs/issue5793.go | interpreted | `complex requires 2 arguments` (interp/gosource_complex.go): `complex(complexArgs())` — a two-result call spread into the builtin | 151 |
| testdir:fixedbugs/issue5856.go | interpreted | `defer called from <path>:17, want issue5856.go:28` — `runtime.Caller` in a deferred call reports the defer site's frame; also the absolute working-copy path where gc prints the relative one | 153 (frames) |
| testdir:fixedbugs/issue71675.go | interpreted | `f recover:called` — recover call lifecycle | 151 |
| testdir:fixedbugs/issue7740.go | interpreted | `invalid native scalar` (interp/bashpp_native_values.go:231): an untyped float constant beyond float64 precision crossing the bridge | 153 |
| testdir:index0.go | interpreted | first line `// run`: the `runoutput ./index.go` generator's output (a program) was treated as the failure text — the recipe's second phase | 153 (runtime/recipe) |
| testdir:iota.go | both | `assertion fail: amask` in both modes: the lowered constant group evaluates `iota` wrongly, so the compiled program fails too | 152/lower (both modes fail identically) |
| testdir:literal2.go | interpreted | `1.7976931348623157e+308 != 1.7976931348623157e+308` — float constant compare/render | 151 (Classic-parity case required) |
| testdir:nilptr.go | interpreted | `fatal error: runtime: out of memory` — a 256 MB array the interpreter materialises | 153; design (D2-shaped) |
| testdir:noinit.go | compiled | exit `1`: the program asserts no init task for constant-initialized package vars (`//go:linkname` to `runtime.main_inittasks`) — the lowered Go initializes them at run time | 152/lower |
| testdir:range.go | interpreted | `Wanted lowercase alphabet; got \x00\x01…` — range over string value evaluation | 151 |
| testdir:range.go | compiled | `wrong parallel assignment 0 99 20` — lowered parallel assignment in a range | 152/lower |
| testdir:range4.go | interpreted | `wrong results [4, 3, 2, 1, 5, -1] want [5, 4, 3, 2, 1, -1]` — range-over-func yield order | 151 |
| testdir:recover3.go | interpreted | `BUG` — recovered runtime panic values | 153 |
| testdir:typeparam/append.go | interpreted | `string for main.Recv` — generic append with a named string-underlying type | 151 |
| testdir:typeparam/dottype.go | interpreted | output `3` — type assertion on a type parameter | 151 |
| testdir:typeparam/issue50690a.go | interpreted | `expression requires one local result, got 0` (interp/bashpp_native_values.go:338) — a multi-result generic call value crossing the bridge | 153 **(corrected: was 151)** |
| testdir:typeparam/mdempsky/13.go | interpreted | `bridge accepts evaluated values, not argument source "Mer(3)"` (interp/bashpp_native_bridge.go:802) | 153 |
| testdir:typeparam/subdict.go | interpreted | `undefined type: comparable` (interp/bashpp_struct.go:60) — the constraint used as a type argument at instantiation | 151 |
| testdir:typeswitch1.go | interpreted | `whatis 1.5 => default 3/2 != default 1.5` — a float reaching the default arm renders as the exact rational | 151 (Classic-parity case required — the Sprint 162 typed-float revert) |
| testdir:zerosize.go | interpreted | `p==q = false` — zero-size allocation pointer identity | 151; likely design |
| typechecker:go/types/TestCheck/decls2 | both | §1 (e) | design, by ID |
| typechecker:go/types/TestCheck/expr3.go | both | §1 (a)+(e) | harness; then design, by ID |
| typechecker:go/types/TestCheck/stmt0.go | both | §1 (d)+(e) | design, by ID |

Rows this lane fixed from the set: `chan/select3.go` (d34872f1) and the
first cause of `blank.go` (b2ec5b41). `issue20298.go` (in 151's manifest,
`class=position`) is a diagnostics row: the recipe's `-e=0` restores gc's
ten-error limit (`base.ErrorfAt`: "too many errors" after the tenth); the
check interface has no such option and the backend hook passes no recipe
flags — owner 154 + harness (a `--go-error-limit` runner fact), not lower.

## 4. Requests to other seams

### 4.1 Harness (bashpp-tests, `tools/upstream-harness/testdata/types-backend/gotypes/`): fold unpositioned continuation lines

Restores the pre-`541f27c` behaviour for unpositioned lines while keeping
the positioned-secondary rule. Verified locally on the patched runner
(§1: 367/371 go/types). The hook pin in `backend-pin.tsv` must be
refreshed with it.

```diff
--- a/tools/upstream-harness/testdata/types-backend/gotypes/bashpp_types_check_test.go
+++ b/tools/upstream-harness/testdata/types-backend/gotypes/bashpp_types_check_test.go
@@ -130,7 +130,23 @@
         // upstream check_test.go ignores (`": \t"`), so it is neither an
         // error nor unattributed here.
         if strings.HasPrefix(line, "\t") {
-			if len(errs) == 0 {
+			// Only a POSITIONED secondary ("\t<file>:<line>:<col>: <msg>") is
+			// the separate Error upstream ignores. An unpositioned
+			// continuation ("\thave ()", "\tT does not implement I (...)")
+			// is part of the primary's own Msg in go/types (errors.go
+			// report: sub-errors without a position are joined into one
+			// message) and the ERROR comments match against it.
+			if bashppDiagRx.MatchString(strings.TrimPrefix(line, "\t")) {
+				if len(errs) == 0 {
+					unparsed++
+				}
+				continue
+			}
+			if n := len(errs); n > 0 {
+				prev := errs[n-1].(Error)
+				prev.Msg += "\n" + line
+				errs[n-1] = prev
+			} else {
                 unparsed++
             }
             continue
```

Unit test to add to `bashpp_types_check_unit_test.go` (same package):

```go
// Sprint: #165; Story: S165.0; an unpositioned TAB line is the primary's own
// continuation (go/types joins sub-errors without a position into one Msg).
func TestBashppParseGotypesFoldsUnpositionedContinuation(t *testing.T) {
	fset, known := gotypesFixture()
	errs, unparsed := bashppParseGotypesDiagnostics(fset, known, "file.go:4:5: not enough arguments in call to f\n\thave ()\n\twant (int)\nfile.go:3:5: \tother declaration of x\n\tfile.go:2:5: previous case\n")
	if unparsed != 0 || len(errs) != 1 {
		t.Fatalf("errs, unparsed = %d, %d; want 1, 0: %v", len(errs), unparsed, errs)
	}
	if got := errs[0].(Error).Msg; got != "not enough arguments in call to f\n\thave ()\n\twant (int)" {
		t.Fatalf("Msg = %q", got)
	}
}
```

### 4.2 Harness + gosource: gc's error limit (`issue20298.go`)

The errorcheck recipe `-e=0` means "at most ten errors, then `too many
errors`" (gc's default; the harness's own `-e` lifts it for every other
root). Proposal: the backend hook passes `--go-error-limit=10` when the
recipe carries `-e=0` (and nothing otherwise), and `gosource.Options`
gains `ErrorLimit int` implementing `base.ErrorfAt`'s rule under the
gc-stderr policy (count non-continuation rows; at the tenth, flush and
append `<pos>: too many errors`). Both halves are needed; this lane did not
add the option without the flag that would exercise it.

### 4.3 interp (151/153): blank.go's next first cause

`type T struct { _, _, _ int }` → `BASHPP-ESTRUCT-FIELD-DUPLICATE: field
"_" declared more than once` (blank fields are never declared; several are
legal), and the native bridge's generated session source emits `_` where
a value or type is expected for the same declarations.

### 4.4 Manager: decisions this lane recommends

- Record by ID, under one decision with `docs/bashpp-source-preserving-
  front-end.md` as the reason: the four typechecker roots of the front-end
  class (`types2 TestCheck/stmt0.go`, `types2 TestLocal/issue47996.go`,
  `types2 TestLocal/issue68183.go`, `go/types TestCheck/stmt0.go`) beside
  `issue23586`/`issue4468`/`issue50372`.
- Record by ID, or decide Route C of the note: `go/types TestCheck/decls2`
  and `expr3.go` ("the go/types runner expects go/parser's columns at
  tolerance 0; the syntax verdict is gc's, D3").

## 5. Recorded, not fixed

- **stmt0's post-statement duplicate.** gc's parser owns "cannot declare in
  post statement of for loop" (a syntax error); types2 skips its own copy,
  go/types reports one at the statement. A structural anchor (drop go/types'
  diagnostic anchored at a `ForStmt.Post` `:=` statement, like the branch
  anchors of `checkerDiagnostics`) would also swallow "non-name x on left
  side of :=" anchored at the same position; the precise key is go/types'
  error code (`InvalidPostDecl`), reachable only through reflection on an
  unexported field. It flips no row while the two "must be function call"
  rows stay missing, so it was left with this note.
- **The Sprint 162 checker-test assertion** (`TestSprint162CheckAfterParser
  Diagnostics`) expected the repeated receiver row and the malformed
  literal under the policy; that expectation was the assumption cause (b)
  disproves and is corrected in 87da0fac.
- **The lower lane's `blank.go` assignment** (plan §Order 1‖, S165.4 wave 1)
  pointed at gosource's guard; the guard is this seam's and is fixed here.
  If the lower lane carries a competing edit of `checkLoweredNames`, take
  this one.

## 6. Verification

- Focused: `go test -count=1 -run TestSprint165 ./gosource/` (six driving
  tests; each fails on the pre-change tree — checked by stashing the
  mechanism).
- Seam: `go test -count=1 -timeout 30m ./gosource/...` before each commit.
- Runners: full go/types + types2 check_test suites through the patched
  runners on this candidate, before and after (verdict diff: additions
  only).
- Full sh gate before the final commit: `go test -short -timeout 30m
  ./interp/ ./lower/ ./gosource/ ./syntax/` (see the lane report for the
  pre-existing darwin failures).
