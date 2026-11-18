# Review: S152.1 identity-lowering commits

Target reviewed: `FETCH_HEAD` from `$HOME/.bashy/weave/sh-7e2e7b65/workspaces/issue-132`, detached scratch worktree `../s132-review`, ending at `27ffe0c4`.

## Findings

### CONCERN: C9 still changes Go evaluation order for comma-ok assertions assigned into non-identifier targets

Commit `faf2f62b` removes the runtime assertion wrapper on the Go-source path, but the converter-side tuple split is still load-bearing and still rewrites comma-ok assertion assignments whose left side is not all identifiers. The fidelity ledger keeps `type-assertion` open, but this is not only textual identity: it is observable for pure Go when the LHS target has side effects.

Relevant sites:

- `gosource/tuple.go:82` documents that the split orders the RHS ahead of the LHS operands.
- `gosource/tuple.go:92` emits temporaries from `x.Rhs` first.
- `gosource/tuple.go:93` then emits the real target assignments.
- `lower/testdata/sprint152/fidelity/type-assertion.go:12` keeps a reproducer shape, `v, *(&ok) = y.(int)`, but without side effects.

Concrete failing input:

```go
package main

import "fmt"

var slot int

func target() *int { fmt.Println("target"); return &slot }
func value() any { fmt.Println("value"); return 1 }

func main() {
	var ok bool
	*target(), ok = value().(int)
	fmt.Println(slot, ok)
}
```

Go evaluates the pointer-indirection operand on the left together with the RHS, left-to-right, so the original prints:

```text
target
value
1 true
```

The split shape still produced by the converter:

```go
__gosource_tuple_0, __gosource_tuple_1 := value().(int)
*target() = __gosource_tuple_0
ok = __gosource_tuple_1
```

prints:

```text
value
target
1 true
```

Verdict for `faf2f62b`: CONCERN. It should not be considered semantically closed for pure Go until tuple assignment lowering preserves LHS/RHS evaluation order for non-identifier targets or is bypassed for Go-source identity mode.

## Commit Verdicts

`e929d13e lower: C5 - keep the source's func main as the Go-source entry`: OK. The behavioral changes are gated by `nativeMain`, which requires `e.goSource` at `lower/callables.go:40`, and synthetic entry-call handling only triggers through `syntheticEntryCall`, which returns false when `!e.goSource` at `lower/callables.go:47`. The generated `init` wrapper at `lower/compile.go:391` is only used for native Go-source main, and init calls are folded before main, preserving Go's var/init/main order for this flattened representation.

`d14cb5d7 lower: keep an empty one-line Go-source body as {} (C11 layout part)`: OK. The one-line empty-body path is gated on `e.goSource` in the body layout helper; no non-goSource behavior change found.

`b4faef52 lower: C10 - keep unnamed and blank receivers and parameters in Go source`: OK. Receiver/parameter synthetic naming remains on the runtime path via `if !e.goSource` at `lower/compile.go:820`, and Go-source unnamed receivers are guarded by the `r.Name != nil` check at `lower/compile.go:831`.

`e5d54578 lower: C2 - no sink statements for Go source`: OK. Sink removal is gated at `lower/compile.go:771` and type-switch binding sinks are gated at `lower/callables.go:762`. The Go-source path now lets gc report unused variables, which is the correct pure-Go behavior rather than a runtime-path compatibility promise.

`c830ec01 lower: C4 - print Go-source expressions with the source's own parentheses`: OK. Parentheses removal is gated through `e.group`, which returns unparenthesized text only when `e.goSource` at `lower/compile.go:1503`; runtime path still wraps. I did not find a precedence drift in the covered expression forms because explicit source parentheses are still represented by `BashPPParenExpr` at `lower/compile.go:1456`.

`faf2f62b lower: C9 - emit Go-source type assertions as plain x.(T)`: CONCERN. Plain value assertions are gated by `e.goSource` at `lower/callables_checked.go:44`, and guard-prologue removal is gated at `lower/callables_checked.go:18`, so the non-goSource path is preserved. However, comma-ok assertions into non-identifier assignment targets still pass through the converter tuple split at `gosource/tuple.go:92`, changing observable evaluation order as shown above.

`7c020fb8 lower: C1 (lower/ half) - carry //go: directives on func and var declarations`: OK. Directive emission is only installed for Go source in `lower/compile.go:266` and `lower/callables.go:235`; the prior `//go:embed` global path remains within the same Go-source block. No non-goSource change found. Scope note: the test is explicitly the lower half; it manually attaches func directives because the converter still does not carry all comments.

`27ffe0c4 lower: lay a one-line Go-source function body out on one line (C11 layout part)`: OK. The new one-line layout path is gated on `e.goSource` at `lower/compile.go:745`. If a statement's emitted marker/body spans multiple lines, it falls back to the old multiline layout at `lower/compile.go:754`.

## Test Adequacy

`lower/fidelity_test.go` matches the branch claims for the landed classes it actively asserts:

- `guard-prologue`, `explicit-deref`, `reparenthesised-exprs`, and `synthetic-receiver-names` are no longer listed in `fidelityOpen`, and the focused fidelity test passes them.
- `main-rename`, `sink-statements`, and `type-assertion` remain listed in `fidelityOpen` because each fixture still differs for another open class. Their removed spellings are tracked in `fidelityLanded` at `lower/fidelity_test.go:42`.

The gap is C9 adequacy: `type-assertion` remains open and demonstrates tuple splitting, but it does not include side effects in the non-identifier LHS target, so it does not catch the evaluation-order drift above.

## Determinism

Command:

```sh
find lower/testdata/sprint152/fidelity -name '*.generated.go' -print | sort | xargs shasum -a 256 > /tmp/s132-generated.before
go test -count=2 ./lower -run TestGoSourceFidelity -v > /tmp/s132-fidelity-count2.out 2>&1
find lower/testdata/sprint152/fidelity -name '*.generated.go' -print | sort | xargs shasum -a 256 > /tmp/s132-generated.after
diff -u /tmp/s132-generated.before /tmp/s132-generated.after
```

Result: `go test -count=2 ./lower -run TestGoSourceFidelity -v` passed twice, and the generated fixture hash diff was empty. The test does not rewrite `.generated.go`; this confirms the archived generated files were stable during the two-run check.

## Required Gate

Command:

```sh
go test -count=1 ./lower/...
```

Result verbatim:

```text
panic: test timed out after 10m0s
	running tests:
		TestGoSourceChannelElementBridge (1s)
		TestGoSourceChannelElementBridge/assigned_field (0s)

goroutine 11688 [running]:
testing.(*M).startAlarm.func1()
	$HOME/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.27.0.darwin-arm64/src/testing/testing.go:2959 +0x2c4
created by time.goFunc
	$HOME/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.27.0.darwin-arm64/src/time/sleep.go:182 +0x38

goroutine 1 [chan receive]:
testing.(*T).Run(0x73022ede66c8, {0x10121452c?, 0x73022ee05a88?}, 0x101944020)
	$HOME/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.27.0.darwin-arm64/src/testing/testing.go:2266 +0x3cc
testing.runTests.func1(0x73022ede66c8)
	$HOME/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.27.0.darwin-arm64/src/testing/testing.go:2742 +0x38
testing.tRunner(0x73022ede66c8, 0x73022ee05bb8)
	$HOME/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.27.0.darwin-arm64/src/testing/testing.go:2193 +0xc4
testing.runTests({0x10120050e, 0xe}, {0x10120656e, 0x14}, 0x73022eda4198, {0x1019c75d0, 0xdd, 0xdd}, {0x73022ee05c58?, 0x73022ed98a80?, ...})
	$HOME/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.27.0.darwin-arm64/src/testing/testing.go:2740 +0x400
testing.(*M).Run(0x73022eda30e0)
	$HOME/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.27.0.darwin-arm64/src/testing/testing.go:2600 +0x578
main.main()
	_testmain.go:488 +0x80

goroutine 5725 [chan receive, 4 minutes]:
testing.(*T).Parallel(0x73022f412248)
	$HOME/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.27.0.darwin-arm64/src/testing/testing.go:1957 +0x194
mvdan.cc/sh/v3/lower_test.TestEntryPassesProcessArgvToShellPositionals(0x73022f412248)
	$HOME/.bashy/weave/sh-7e2e7b65/workspaces/s132-review/lower/callables_entry_positional_test.go:105 +0x28
testing.tRunner(0x73022f412248, 0x101943fe8)
	$HOME/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.27.0.darwin-arm64/src/testing/testing.go:2193 +0xc4
created by testing.(*T).Run in goroutine 1
	$HOME/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.27.0.darwin-arm64/src/testing/testing.go:2258 +0x3b8

goroutine 11607 [chan receive]:
testing.(*T).Run(0x73022ede6488, {0x10120098a?, 0x73022ed6ff58?}, 0x101944300)
	$HOME/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.27.0.darwin-arm64/src/testing/testing.go:2266 +0x3cc
mvdan.cc/sh/v3/lower_test.TestGoSourceChannelElementBridge(0x73022ede6488)
	$HOME/.bashy/weave/sh-7e2e7b65/workspaces/s132-review/lower/gosource_tuple_channel_callable_test.go:154 +0x50
testing.tRunner(0x73022ede6488, 0x101944020)
	$HOME/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.27.0.darwin-arm64/src/testing/testing.go:2193 +0xc4
created by testing.(*T).Run in goroutine 1
	$HOME/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.27.0.darwin-arm64/src/testing/testing.go:2258 +0x3b8

FAIL	mvdan.cc/sh/v3/lower	600.328s
ok  	mvdan.cc/sh/v3/lower/shellrt	5.610s
ok  	mvdan.cc/sh/v3/lower/shellrt/shellexec	3.386s
FAIL
```

## Merge Recommendation

Do not merge as-is. The non-goSource path looked properly gated, and most reviewed commits are OK, but the C9 comma-ok tuple split still has a pure-Go semantic drift with a concrete side-effecting LHS reproducer. The required lower gate also timed out on this checkout, so this branch needs at least a C9 fix or explicit deferral plus a resolved/understood lower test gate before merging.

Sprint: #152
Story: #77
Story-ID: 9a27ae296c91
