# Sprint 171 lane w3-fidelity findings

The exact Go 1.27 harness owns final root verdicts. The native-constant
mechanism has two emitter sites: grouped constants and individual constants.
Compiled Go now selects the written `Init` carrier at both sites, while the
interpreter continues to consume the typed, possibly folded `InitExpr`.

| root | mode | first cause | mechanism | status |
|---|---|---|---|---|
| `testdir:fixedbugs/bug273.go` | compiled | folding the function-local `unsafe.Sizeof` removed the import's only use | retain a written imported-const initializer | fixed in `5428f185` (current-tree manual transpile passed) |
| `testdir:fixedbugs/issue22344.go` | compiled | grouped constants emitted the contextualized tree, which replaced nested `iota` expressions and erased lexical uses | emit a native const group's written initializer carrier | fixed in `665fc9c9` |
| `testdir:fixedbugs/issue54911.go` | compiled | `gosource.converter.callee` moves receiver type arguments in `Set[T].Add(s)` onto the method and produces `Set.Add[T](s)` | preserve an instantiated receiver as the callee expression | moved to `gosource` (F171-C2) |
| `testdir:fixedbugs/issue7794.go` | compiled | `gosource.valueDecl` stores the folded value `10` in both initializer carriers, so lower never receives `len(a)` | retain written `Init` plus folded `InitExpr`; lower selects written native carrier | moved to `gosource`; lower prerequisite fixed in `2b7c3833` (F171-C3) |
| `testdir:shift3.go` | compiled | folding `math.MaxUint` removed the import's only use | retain a written imported-const initializer | fixed in `5428f185` (current-tree manual transpile passed) |
| `package:cmd/compile/internal/ir` | compiled | library emission folded a function-local imported constant and retained the now-unused import | retain a written imported-const initializer per library file | fixed in `5428f185`; covered by the outside-corpus library build test (leaf revalidation required) |
| `testdir:live_regabi.go` | compiled | select communication conversion calls `converter.one`; the dereferenced tuple receive assignment expands to several statements and is rejected as `unsupported compound simple statement` before lower runs | preserve a tuple receive assignment as one select communication command | moved to `gosource` + syntax carrier (F171-C4) |
| `testdir:rangegen.go` | compiled | gc resource exhaustion is not explained by changed generator output | no bounded emitter fix: native and lowered generators emit identical compiler input | design for Sprint 174 (F171-C5) |

## F171-C5 measurements

The original generator is 8,528 bytes with 9 top-level declarations, 5
functions, and 6,246 function-body bytes. The lowered generator is 20,997
bytes with 15 top-level declarations, 5 functions, and 18,544 function-body
bytes; the additional declaration nodes are split imports and the body growth
is source-map marker and line-directive scaffolding, not duplicated functions.
Most importantly, running either generator produces exactly 6,399,112 bytes
and 242,393 lines. The generated compiler inputs are byte-for-byte equal, so
the measured gc timeout/OOM has no emitter cause and no timeout was raised.

## Requests to other seams

### gosource: retain the individual constant's written carrier

In `converter.valueDecl`, when an explicit const initializer exists, assign
`out.Init = []*s.Word{c.word(v.Values[index])}` before choosing `InitExpr`.
In the folded `types.Const` branch, remove the later assignment that replaces
`out.Init` with `obj.Val().ExactString()`; keep the folded `InitExpr` exactly as
it is for interpretation. This supplies `2b7c3833` the written `len(a)` carrier
without changing interpreter evaluation.

### gosource: keep type arguments on an instantiated receiver

In `converter.call`'s `simple` predicate, return false for an
`*ast.SelectorExpr` whose `X` is an `*ast.IndexExpr` or
`*ast.IndexListExpr` and whose `c.info.Selections` entry is non-nil. That sends
`Set[T].Add` through `out.CalleeExpr = c.expr(x.Fun)` instead of flattening it
through `callee` into `Fun={Set,Add}, TypeArgs={T}`. Add the corresponding
pointer-parenthesized receiver cases to the negative set.

### gosource + syntax: preserve select tuple receive assignment as one command

`converter.one(cc.Comm)` cannot represent `*p, *ok = <-ch`: the ordinary
statement path deliberately expands it into temporaries, which is illegal in a
select communication header. Add `TargetExprs []BashPPExpr` to
`syntax.BashPPAssign`; in the select conversion path, construct one assignment
with every original LHS in `TargetExprs` and the existing `Recv` carrier,
without calling `tupleAssignStmts`. The lower command emitter should join
those target expressions and emit the single legal `lhs... = <-ch` header.
The interpreter can continue using its existing select receive-assignment
scheduler after extending its target commit loop to the same slice.
