# Sprint 151 / Story #75 — M2 spike: why `gosource` rejects ordinary Go

Bounded spike (18-minute cap), findings first, no product fix. Each mechanism
below has one small **out-of-corpus** Go program in this directory (all are
`go vet` clean and print a deterministic result under real Go) and one row in
`sh/gosource/sprint151_spike_test.go`, which pins the converter's *current*
verdict so the test is green now and flips when a mechanism lands.

Fail sites are quoted from `sh/gosource/convert.go` at the commit of this
spike (`c.fail` keeps only the **first** error per program, in source order —
this matters for attribution, see "Attribution caveats").

Corpus root counts are the Sprint 149/150 ledger numbers given in the story;
`../bashpp-tests` is not available in this workspace, so they are not re-counted.

Syntax reality check: `sh/syntax` has **no label node**. `BashPPBranch`
(`syntax/bashpp_nodes.go:1001`) is documented as "an unlabeled Go-form break,
continue, or fallthrough" and carries only `Kw *Lit`; the interpreter keeps a
single `r.bashPPBranch` flag with no depth or target (`interp/bashpp_p1.go:29`).
There is no `goto`, no `LabeledStmt`, and no label on `BashPPFor`,
`BashPPRange`, `BashPPSwitch`, or `BashPPSelect`.

Ledger total attributed to these sites: ~65 roots.

---

## 1. Labeled `break` / `continue` — ~30 roots ("unsupported LabeledStmt" 25 + "unsupported labeled branch" 5, shared with §2)

**Fail sites**
- `convert.go:808` — `statements` default arm: `*ast.LabeledStmt` has no case, so
  the *label* fails first (`unsupported LabeledStmt`) in every program where the
  label textually precedes the branch, i.e. all labeled `break`/`continue`.
- `convert.go:756` — `*ast.BranchStmt` with `x.Label != nil` → `unsupported labeled
  branch`. Only reached when the branch precedes its label (forward `goto`, §2) —
  for labeled loops it is masked by :808.

**Reproducers**: `labeled_break_for.go`, `labeled_continue_for.go`,
`labeled_break_switch.go` (break out of a `for` from a `switch` arm — the case
a bare `break` cannot express), `labeled_break_select.go` (same from `select`).

**Bash++ node**: none today. Two options:
- (a) *No new node.* Add `Depth int` (or reuse a `Level *Lit`) to `BashPPBranch`,
  mirroring bash's `break N`/`continue N`. The converter counts, from the branch
  up to the labeled statement, the number of enclosing *breakable* constructs
  (`for`, `range`, `switch`, `type switch`, `select` for `break`; `for`/`range`
  only for `continue` — a `continue L` inside a `switch` inside loop L is depth 1
  for the loop but the switch must be unwound too, so the interpreter's unwind
  must treat `switch`/`select` as transparent for `continue`, exactly as bash's
  `case` is). `LabeledStmt` itself converts to its inner statement.
- (b) *Label-preserving.* `BashPPLabeled{Label *Lit; Stmt}` + `Label *Lit` on
  `BashPPBranch`. Faithful, needed anyway for `goto` (§2) and for `lower/`
  round-tripping to Go source, but touches parser/printer/typedjson/lower.

**Recommendation**: (a) for the converter + interpreter (1 day): it reuses the
`breakEnclosing` counting the runner already has for bash `break N`
(`interp/runner.go:6509`, `:10567`) and needs no parser change since Go-source
programs never go through the Bash++ parser. `lower/compile.go:1108` emits
`n.Kw.Value` today; with depth it must synthesize a label on the target loop,
which is a small, mechanical addition. Then revisit (b) when §2 is designed so
both share one label carrier.

**Estimate**: 1 day (converter depth counting + `BashPPBranch.Depth` + interp
unwind + lower synthesized label + flip the four spike rows).

**Unlocks**: most of the 25 `LabeledStmt` roots (only the backward-`goto`
programs among them stay red — see §2).

## 2. `goto` (forward and backward) — subset of the same ~30 roots

**Fail sites**: `convert.go:756` for forward `goto` (branch seen first:
`goto_forward.go:10:3: unsupported labeled branch`), `convert.go:808` for
backward `goto` (label seen first: `goto_backward.go:8:1: unsupported LabeledStmt`).
The 5 "labeled branch" roots are therefore an upper bound on forward-`goto`
programs; backward `goto` is indistinguishable from labeled loops in the ledger.

**Reproducers**: `goto_forward.go`, `goto_backward.go`.

**Bash++ node**: none, and it cannot be simulated with `break N`. Needs:
a label carrier (§1 option b), a `BashPPGoto` statement, and an interpreter
execution model for arbitrary jumps inside a block list — the runner walks the
AST recursively, so a `goto` to a sibling label needs at minimum a
"restart this block at statement k" unwind signal (forward and backward within
the same block are the Go-legal cases; jumping *into* a block is illegal Go,
which keeps the design bounded to block-local jumps plus outward unwinding).

**Recommendation**: design item, not a 1-day fix. Cheapest partial: forward
`goto` whose label is the last statement of the same block can be lowered to
`if !cond { … }` restructuring, but that is a source rewrite, not a converter
change. Do §1 first and re-measure: the residual `LabeledStmt` count then
*is* the backward-`goto` count.

**Estimate**: design (syntax + interp + lower).

**Landed** (S151.4): `BashPPLabeled` + `BashPPGoto`, block-list restart in
`Runner.stmts`, verbatim labels in `lower`. Both spike rows flipped; see
`goto/FINDINGS.md` for the mechanism and the 38-root measurement.

## 3. Expression statement — 12 roots

**Fail site**: `convert.go:598` — `*ast.ExprStmt` whose `X` is neither
`*ast.CallExpr` nor a receive `*ast.UnaryExpr{Op: ARROW}`.

**Finding**: the story's suggested reproducers (bare conversion, method value)
are **not valid Go** (`x.M` / `T(x)` "is not used"); Go admits only calls and
receives in statement position. So the legal shapes are:

1. `exprstmt_bare_typeswitch.go` — **`switch v.(type)` with no binding.**
   go/ast stores the guard as an `*ast.ExprStmt{X: *ast.TypeAssertExpr}` in
   `TypeSwitchStmt.Assign`; `convert.go:780` passes it to `c.one`, which routes
   it into `statements` and hits :598. This is by far the likeliest source of
   the 12 roots — it is a very common Go idiom.
2. `exprstmt_paren_call.go` — parenthesized call `(f())` / receive `(<-ch)`:
   `*ast.ParenExpr` outer node. Rare in practice.

**Bash++ node**: already exists — `BashPPSwitch{TypeSwitch: true, Init: …}`
carries the guard. For (1) the converter should build the guard from the
`TypeAssertExpr` directly (a `BashPPTypeAssertExpr` with `TypeToken`, no
short-decl) instead of going through `c.one`; the runner already evaluates a
bound guard, so an unbound one is the same evaluation minus the binding. For
(2), `ast.Unparen(x.X)` before the call/receive checks at :598.

**Estimate**: 1 day for both (half a day each; (2) is a one-liner plus a test).
Runner/lower work for (1) depends on how `Init == nil` type switches are
handled downstream — verify with the spike row before declaring done.

**Unlocks**: up to 12 roots; expect most from (1).

## 4. Expression kind `*ast.<X>` — 12 roots

**Fail site**: `convert.go:524` — `exprValue` default. Handled kinds: FuncLit,
BasicLit, Ident, ParenExpr, UnaryExpr, StarExpr, BinaryExpr, SelectorExpr,
IndexExpr, SliceExpr, CompositeLit, TypeAssertExpr, CallExpr. Every other
`ast.Expr` that can appear in value position is a *type expression* or
`IndexListExpr`.

**Reproducers** (two distinct shapes found):
1. `expr_typeswitch_composite_case.go` — a type-switch `case []int:` /
   `case map[string]int:` / `case func():`. `convert.go:785` converts each
   case expression through `c.expr`, which has no type cases, so unnamed
   composite types fail (`*ast.ArrayType` first; `MapType`, `FuncType`,
   `ChanType`, `StructType`, `InterfaceType` are the same bug). Named types
   (`case int:`) survive only because an `Ident` converts as a `BashPPIdent`.
2. `expr_indexlist_funcvalue.go` — `f := pair[string, int]`: a generic
   function instantiated with ≥2 explicit type args used as a *value*.
   (`f := g[int]` with one arg does not fail but converts as a
   `BashPPIndexExpr`, which is semantically wrong — same fix.)

**Bash++ node**: for (1), `BashPPSwitchArm.Exprs []BashPPExpr` is the wrong
carrier for a type; a type switch arm should carry `BashPPTypeExpr`. Least
invasive: when `out.TypeSwitch`, convert `cc.List` through `c.typ` and store in
a new `BashPPSwitchArm.Types []BashPPTypeExpr` (or wrap in the existing
`BashPPConvertExpr`/named-type ident so no node changes — check what the runner
matches on). For (2), `BashPPCall` already has `TypeArgs`; a value-position
instantiation wants the same on `BashPPIdent`/`BashPPSelectorExpr` (add
`TypeArgs []*BashPPTypeArg`) and the `IndexExpr` case at :472 should detect
`c.info.Types[x.X].IsType()`-style instantiation (`c.info.Instances[id]`) and
route there too.

**Estimate**: (1) 1 day; (2) 1 day if the runner already instantiates generics
on call, otherwise design. Remaining `*ast.<X>` kinds not reproduced in-bound
(`Ellipsis`, `KeyValueExpr` outside literals) are not legal value expressions.

**Unlocks**: up to 12 roots, likely dominated by (1).

## 5. Type kind `*ast.<X>` — 3 roots

**Fail site**: `convert.go:254` — `typ` default. But the *caller* is the bug:
`convert.go:556` (`call`/`callee`, `*ast.IndexExpr` arm) treats **every**
indexed callee as a generic instantiation and hands the index to `c.typ`.

**Reproducer**: `type_indexed_call.go` — `handlers["one"]()` fails with
`unsupported type *ast.BasicLit`; `steps[i+1]()` would fail with
`*ast.BinaryExpr`. Worse, `steps[i]()` does *not* fail: the index `i` becomes a
`BashPPNamedType{Name: i}` type argument — silently wrong, not just rejected.

**Bash++ node**: `BashPPCall.CalleeExpr` already exists for non-simple callees.
Fix in `simple`/`callee` (:539, :556): treat `IndexExpr`/`IndexListExpr` as a
callee only when `c.info.Types[v.X].IsType()` is false **and**
`c.info.Instances[ident]` records an instantiation (or, simpler, when
`c.info.Types[v.Index].IsType()`); otherwise fall to `CalleeExpr = c.expr(x.Fun)`.

**Estimate**: half a day, plus the silent-wrong case deserves its own regression row.

**Unlocks**: 3 roots (+ an unknown number of silently-miscompiled `fs[i]()` calls).

## 6. Range assignment target — 4 roots

**Fail site**: `convert.go:749` — `*ast.RangeStmt` key/value that is not an
`*ast.Ident` (`for a[i] = range`, `for _, s.f = range`, `for *p = range`;
Go permits any addressable operand with `=`, and `Names` only holds `*Lit`).

**Reproducers**: `range_target_index.go`, `range_target_field.go`,
`range_target_deref.go`.

**Bash++ node**: `BashPPRange{Names []*Lit; Define Pos}`. Add
`Targets []BashPPExpr` (parallel to `Names`, used when `Define` is unset) and
have the runner assign through the existing lvalue path used by `BashPPAssign.TargetExpr`.
Alternative without a node change: convert to a fresh-temp `range` plus a
`BashPPAssign` as the first body statement — no runner work, and the
`c.prefix` temp convention from `tuple.go` already exists. That rewrite is the
1-day path.

**Estimate**: 1 day (temp+assign rewrite in the converter).

**Unlocks**: 4 roots.

## 7. Compound simple statement — 2 roots

**Fail site**: `convert.go:821` — `one` requires `statements(st)` to yield
exactly one command. It yields several when the header statement is a tuple
assignment with a non-identifier target (`tuple.go:87 tupleAssignStmts`) or a
multi-name `var` with a single call initializer (`tupleValueDecls`, only in
`DeclStmt`, which cannot be a simple statement). Callers: `if` init (:727),
`for` init/post (:741), `select` comm (:769), `switch` init (:794),
type-switch guard (:780).

**Reproducer**: `forinit_compound.go` — `for a[0], i = 5, 0; …`.

**Bash++ node**: `BashPPFor.Init`/`Post` and `BashPPIf.InitStmt` are single
`Command`s. Cheapest: let `one` return a `*syntax.Block` wrapping the several
statements when `len(v) > 1` — the runner already executes a `Block` as a
command and header scoping is preserved because `tupleAssignStmts` temps use
the collision-free prefix. `lower` must then flatten the block into the Go
header (`a0, i = 5, 0`), which it can do by re-tupling, or fall back to
hoisting the init above the loop (legal only for `Init`, not `Post`).

**Estimate**: 1 day; `Post` with a compound statement is the awkward corner
(bash `for ((;;))` has no post-block either — hoist to end of body).

**Unlocks**: 2 roots.

## 8. Function value type — 2 roots

**Fail site**: `function_values.go:33` — `parser.ParseExpr` cannot parse the
`types.TypeString` of a signature. The comment there says named func types
(`iter.Seq[T]`) are the concern; they are **fine** — `funcvalue_named_type.go`
(a `type Handler func(int) int` value) converts cleanly and is pinned as the
one *passing* row. What actually fails is an **uninstantiated generic
signature**: `TypeString` spells it `func[S ~[]E, E cmp.Ordered](x S) E`,
which is not a parsable expression.

**Reproducer**: `funcvalue_generic_selector.go` — `f := slices.Max[[]int]`.
The `IndexExpr` converts `x.X` (`slices.Max`) as a `BashPPSelectorExpr`, whose
`FuncType: c.functionValueType(x)` asks for the type of the *generic* selector.

**Bash++ node**: `BashPPSelectorExpr.FuncType`/`BashPPCall.ResultFuncType` are
fine carriers. Fix: in `functionValueType`, if `typeSignature.TypeParams().Len() > 0`
return nil (the enclosing `IndexExpr`/`Call` knows the instantiation via
`c.info.Instances` and should carry the instantiated signature instead — same
change as §4(2)).

**Estimate**: half a day for the guard (stops the rejection); full value
semantics ride on §4(2).

**Unlocks**: 2 roots.

---

## Attribution caveats

- `c.fail` records the first error only, so ledger buckets are *first-hit*
  counts: a program with a labeled loop **and** a bare type switch is counted
  once under whichever appears first. Expect bucket sizes to shift (and new,
  smaller buckets to appear) after §1 lands; re-run the ledger after each fix
  rather than trusting the residual arithmetic.
- `unsupported LabeledStmt` (25) conflates §1 and backward `goto` (§2); only
  the post-§1 residual tells them apart.

## Suggested order (cheapest roots first)

1. §3(1) bare type switch — half a day, likely ~10 roots.
2. §1 labeled break/continue via depth — 1 day, likely ~20+ roots.
3. §5 indexed call — half a day, 3 roots **plus** a silent miscompile.
4. §4(1) composite type-switch cases — 1 day.
5. §6 range targets, §7 compound header, §8 generic signature guard — 1 day each / half.
6. §2 `goto` and §4(2) generic function values — design items.

## Not characterized in bound

- `convert.go:565` "call target", `:778` "type switch initializer",
  `:419` "tuple variable declaration", `:314`/`:321` receiver forms, `:84`
  string literal, `:158`/`:372` inferred types — not in the story's ledger
  buckets, not probed.
- `convert.go:472` — `IndexExpr` on a generic *function* with one type arg
  silently converts as an index (see §4(2)); observed by reading, no
  behavioural probe written ("over bound" for an interpreter run).
