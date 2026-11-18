# Sprint 152 — converter-side (gosource) LOWER-row triage

Localizations of the active-manifest LOWER rows whose owner is the
converter (gosource/). Fixed rows carry their commit; rows whose cause is in
`lower/` (the emitter) are written up here for that owner instead of fixed,
per the S152.2 contract.

Corpus: `$HOME/projects/poc/go/test`. Transpiler: a `bashy.real`
built from `../bashy` against this checkout. Each reproducer below is an
out-of-corpus reduction (5–15 lines) transpiled with
`bashy.real transpile --bashpp --source=go --go-file <root> -o <out> --map <out>.map`.

## Fixed in gosource/ (this story)

| row | corpus root | reproducer | fix |
|---|---|---|---|
| LOWER-EUNDEFINED: undefined: _ | fixedbugs/bug420.go | testdata/sprint152/blank-target-paren | unwrap parenthesized assignment targets (`(_) = v`) |
| LOWER-EUNDEFINED: undefined: new | fixedbugs/issue63436.go | testdata/sprint152/new-paren | unwrap parenthesized `new` builtin callee (`(new)(T)`) |
| LOWER-ETYPE: C2.P undefined (untyped int …) | fixedbugs/bug439.go | testdata/sprint152/const-defined-type | a const initialized from a defined-type value inherits that type |
| LOWER-ETYPE: cannot call int (untyped int constant …) | rename.go | testdata/sprint152/shadowed-builtin-const | skip the C3 constant wrap when its predeclared type name is redeclared at package scope |

## Cause is in lower/ — for the emitter owner

### LOWER-EUNDEFINED: undefined: T — routing a type assertion to a generic type

Roots: `typeparam/issue52026.go` (`_ = s.(Some[int])`, `_ = s.(None)` on a
value of generic-interface static type `Option[int]`).

Reduction (transpiles to `undefined: T` at the assertion line):

```go
package main

type Option[T any] interface{ sealed() }
type Some[T any] struct{ val T }

func (s Some[T]) sealed() {}

func ret[T any]() Option[T] { return Some[T]{} }

func main() {
	s := ret[int]()
	_ = s.(Some[int]) // LOWER-EUNDEFINED: undefined: T
}
```

Localization: the converter spells the asserted type correctly. `c.typ` on
`Some[int]` yields `BashPPNamedType{Some, TypeArgs:[int]}` — the concrete
`int`, never the receiver's type parameter `T` — and the same reduction
without the assertion (`_ = s`) lowers and runs clean. The `T` only appears
once the assertion is routed through the runtime (C9,
`lower/checked_values.go` / `lower/callables_checked.go`): the generated
`shellrt.Assert[…]`/method-set spelling for the generic target type
`Some[int]` reintroduces the declaration's `T`. This is the C9 emitter path
the Spike-F FINDINGS marks "design — the checked assertion is the runtime's
diagnostic contract"; pure-Go mode needs a decision on emitting a plain
`x.(T)` (and here spelling the generic target with its instantiated type
argument, not the declaration's parameter).

## Out of single-root scope

`fixedbugs/bug306.go` and `fixedbugs/issue24801.go` are `// compiledir`
roots: they compile against a companion `.dir` package and are not
standalone Go files, so a single-file transpile parse-fails
(`expected 'package', found ignored` / `expected ';', found 'EOF'`) before
the converter runs. These need the dir-mode harness, not a converter change.
