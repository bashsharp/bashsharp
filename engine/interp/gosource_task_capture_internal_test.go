// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// parseGoStmt returns the single `go ...` statement in src.
func parseGoStmt(t *testing.T, src string) *syntax.BashPPGo {
	t.Helper()
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	var found *syntax.BashPPGo
	syntax.Walk(file, func(node syntax.Node) bool {
		if g, ok := node.(*syntax.BashPPGo); ok && found == nil {
			found = g
		}
		return true
	})
	if found == nil {
		t.Fatalf("no go statement in %q", src)
	}
	return found
}

func scalarCell(value string) *bashPPCell {
	return &bashPPCell{vr: expand.Variable{Kind: expand.String, Str: value}}
}

// captureRunnerFor builds a GoSource runner whose lexical scope holds names.
func captureRunnerFor(names ...string) (*Runner, map[string]*bashPPCell) {
	r := &Runner{bashPPGoSource: true, bashPPScope: newBashPPScope(nil)}
	cells := make(map[string]*bashPPCell, len(names))
	for _, name := range names {
		cell := scalarCell("0")
		r.bashPPScope.entries[name] = cell
		cells[name] = cell
	}
	return r, cells
}

// TestGoSourceCaptureFreeVariablesOnly is the core rule: a variable the body
// mentions freely is captured by reference, and a parameter is not. Excluding
// parameters is what keeps `go func(n int){…}(i)` from sharing the parent's
// still-advancing loop variable, which Go does not share either.
func TestGoSourceCaptureFreeVariablesOnly(t *testing.T) {
	r, cells := captureRunnerFor("counter", "i")
	g := parseGoStmt(t, "func main() {\n\tgo func(n int) {\n\t\tcounter++\n\t}(i)\n}\n")
	shared, _ := r.bashPPGoSourceTaskCapture(g.Call)
	if !shared[cells["counter"]] {
		t.Error("free variable counter was not captured by reference")
	}
	if shared[cells["i"]] {
		t.Error("loop variable i is passed by value, so it must not be shared")
	}
}

func TestGoSourceRangeYieldCallbackCaptureUsesRangeBody(t *testing.T) {
	program, err := gosource.Parse(strings.NewReader(`package main

var pkg int
var values []int

func main() {
	var acc int
	var v int
	for _, v := range values {
		acc += v
		pkg += acc
	}
	_ = v
}
`), "range-yield.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var rng *syntax.BashPPRange
	syntax.Walk(program.File, func(node syntax.Node) bool {
		if candidate, ok := node.(*syntax.BashPPRange); ok && rng == nil {
			rng = candidate
		}
		return true
	})
	if rng == nil {
		t.Fatal("converted source has no range statement")
	}

	scope := newBashPPScope(nil)
	acc := scalarCell("0")
	outerV := scalarCell("1")
	pkg := scalarCell("2")
	scope.entries["acc"] = acc
	scope.entries["v"] = outerV
	scope.entries["pkg"] = pkg

	runner := &Runner{bashPPGoSource: true, bashPPGoSourceFile: program.File}
	fn := &bashPPFunc{
		rangeYield: &goSourceRangeYield{rng: rng},
		lit: &syntax.BashPPFuncLit{
			Params:  []*syntax.BashPPField{{FieldTypeExpr: bashPPRangeNamedType("int")}},
			Results: []*syntax.BashPPField{{FieldTypeExpr: bashPPRangeNamedType("bool")}},
		},
		scope: scope,
	}
	if pos := bashPPCallbackSourcePos(fn); !pos.IsValid() || pos != rng.Pos() {
		t.Fatalf("synthetic callback source position = %v, want range position %v", pos, rng.Pos())
	}
	capture, exact := runner.bashPPCallbackFunctionCapture(fn)
	if !exact {
		t.Fatal("range-yield callback capture was not exact")
	}
	capture = runner.bashPPCallbackSharedCells(fn, capture)
	if !capture[acc] || !capture[pkg] {
		t.Fatalf("range-yield callback lost outer cells: acc=%v pkg=%v", capture[acc], capture[pkg])
	}
	if capture[outerV] {
		t.Fatal("range loop variable captured an outer shadow")
	}
}

// TestGoSourceCaptureFlowsThroughCalledClosures is the combined
// capture+WaitGroup regression (Sprint #118, Story #54) pinned at the
// capture-set level.
//
// The launched body names only a closure; the closure's own body frees a
// local struct holding a native mutex. Go grants identity through the call
// chain — the goroutine reaches everything bump captures the moment it calls
// through — so the struct cell must be shared, not deep copied: copying it to
// protect the descriptor copied away the ORIGINAL counters map beside it,
// and the three-way synchronized original printed map[a:0 b:0] where Go
// prints map[a:20000 b:10000]. A DIRECT native handle freed by the same
// closure keeps the bashpp_task.go descriptor-copy rule.
func TestGoSourceCaptureFlowsThroughCalledClosures(t *testing.T) {
	r, cells := captureRunnerFor("box", "gate")
	cells["box"].vr = expand.Variable{Kind: expand.Object, Obj: map[string]any{
		"mu":       &bashPPBridgeValue{Kind: "handle", Type: "sync.Mutex", Session: "s", Handle: 1},
		"counters": map[string]any{"a": 0},
	}}
	cells["gate"].vr = expand.Variable{Kind: expand.Object,
		Obj: &bashPPBridgeValue{Kind: "handle", Type: "sync.Mutex", Session: "s", Handle: 2}}
	src := `package main
import "sync"
var box struct{counters map[string]int}
var gate sync.Mutex
func main() {
	bump := func() {
			box.counters["a"]++
		}
	go func() {
		bump()
	}()
}
`
	program, err := gosource.Parse(strings.NewReader(src), "capture.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var literals []*syntax.BashPPFuncLit
	var goStmt *syntax.BashPPGo
	syntax.Walk(program.File, func(node syntax.Node) bool {
		switch node := node.(type) {
		case *syntax.BashPPFuncLit:
			literals = append(literals, node)
		case *syntax.BashPPGo:
			goStmt = node
		}
		return true
	})
	if len(literals) != 2 || goStmt == nil {
		t.Fatalf("parsed %d literals and go=%v from %q", len(literals), goStmt != nil, src)
	}
	fn := &bashPPFunc{lit: literals[0], scope: r.bashPPScope, bound: "bump"}
	r.bashPPClosures = append(r.bashPPClosures, fn)
	handle := bashPPFuncHandlePrefix + "0"
	cells["bump"] = scalarCell(handle)
	r.bashPPScope.entries["bump"] = cells["bump"]

	shared, _ := r.bashPPGoSourceTaskCapture(goStmt.Call)
	if r.exit.err != nil {
		t.Fatal(r.exit.err)
	}
	if !shared[cells["bump"]] {
		t.Error("the closure the launched body calls must itself be captured by reference")
	}
	if !shared[cells["box"]] {
		t.Error("a local struct holding a native field is one original Go variable; " +
			"deep copying it copies away the original map/slice fields the program increments")
	}
	if shared[cells["gate"]] {
		t.Error("an unused gate must not enter this closure capture set")
	}
}

// TestGoSourceCaptureRefusesInexactCalledClosure keeps the exact-or-nothing
// rule on the transitive surface: a closure the body calls through whose own
// body contains an unmodelled construct refuses the launch rather than
// silently deep-copying what Go shares.
func TestGoSourceCaptureRefusesInexactCalledClosure(t *testing.T) {
	r, cells := captureRunnerFor("box")
	cells["box"].vr = expand.Variable{Kind: expand.Object, Obj: map[string]any{"n": 1}}
	src := `func main() {
	bump := func() {
		echo hello
		box.n++
	}
	go func() {
		bump()
	}()
}
`
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	var literals []*syntax.BashPPFuncLit
	var goStmt *syntax.BashPPGo
	syntax.Walk(file, func(node syntax.Node) bool {
		switch node := node.(type) {
		case *syntax.BashPPFuncLit:
			literals = append(literals, node)
		case *syntax.BashPPGo:
			goStmt = node
		}
		return true
	})
	if len(literals) != 2 || goStmt == nil {
		t.Fatalf("parsed %d literals and go=%v from %q", len(literals), goStmt != nil, src)
	}
	fn := &bashPPFunc{lit: literals[0], scope: r.bashPPScope, bound: "bump"}
	r.bashPPClosures = append(r.bashPPClosures, fn)
	cells["bump"] = scalarCell(bashPPFuncHandlePrefix + "0")
	r.bashPPScope.entries["bump"] = cells["bump"]

	shared, _ := r.bashPPGoSourceTaskCapture(goStmt.Call)
	if shared != nil {
		t.Fatalf("an inexact called closure must refuse the launch, got %d shared cells", len(shared))
	}
	if r.exit.err == nil {
		t.Fatal("the refusal must carry the unsupported-capture diagnostic")
	}
}

// TestGoSourceCaptureShadowedParameter checks that a parameter shadowing an
// outer name of the same spelling still resolves to the parameter, so the
// outer cell keeps its copy semantics.
func TestGoSourceCaptureShadowedParameter(t *testing.T) {
	r, cells := captureRunnerFor("n")
	g := parseGoStmt(t, "func main() {\n\tgo func(n int) {\n\t\tn++\n\t}(n)\n}\n")
	if shared, _ := r.bashPPGoSourceTaskCapture(g.Call); shared[cells["n"]] {
		t.Error("a shadowing parameter must not grant identity to the outer n")
	}
}

// TestGoSourceCaptureClassicBashPPUnchanged is the guard on the blast radius.
// Outside GoSource mode a task is a shell construct with a private copy of the
// shell, and that deep-copy snapshot must stay exactly as it was.
func TestGoSourceCaptureClassicBashPPUnchanged(t *testing.T) {
	r, _ := captureRunnerFor("counter")
	r.bashPPGoSource = false
	g := parseGoStmt(t, "func main() {\n\tgo func() {\n\t\tcounter++\n\t}()\n}\n")
	if shared, _ := r.bashPPGoSourceTaskCapture(g.Call); shared != nil {
		t.Errorf("classic Bash++ must keep the deep-copy snapshot, got %d shared cells", len(shared))
	}
}

// A descriptor without a live minting session cannot gain authority merely
// because an original function mentions the variable holding it.
func TestGoSourceCaptureRejectsUnboundNativeHandles(t *testing.T) {
	r, _ := captureRunnerFor()
	handle := &bashPPCell{vr: expand.Variable{
		Kind: expand.Object,
		Obj:  &bashPPBridgeValue{Kind: "handle", Type: "sync.Mutex", Session: "s", Handle: 1},
	}}
	r.bashPPScope.entries["mu"] = handle
	g := parseGoStmt(t, "func main() {\n\tgo func() {\n\t\tmu.Lock()\n\t}()\n}\n")
	if shared, _ := r.bashPPGoSourceTaskCapture(g.Call); shared[handle] {
		t.Error("a native handle without a live minting session must be rejected")
	}
}

// A channel from no task group cannot be captured into the current group.
func TestGoSourceCaptureRejectsForeignChannels(t *testing.T) {
	r, _ := captureRunnerFor()
	ch := &bashPPCell{channel: &bashPPChannel{}}
	r.bashPPScope.entries["ch"] = ch
	g := parseGoStmt(t, "func main() {\n\tgo func() {\n\t\tch <- 1\n\t}()\n}\n")
	if shared, _ := r.bashPPGoSourceTaskCapture(g.Call); shared[ch] {
		t.Error("a channel without a matching task-group owner must be rejected")
	}
}

// TestGoSourceCaptureClonerAliasesSharedCell is the scope hook itself. A
// captured cell must come out of the clone as the SAME pointer, and every
// other edge which reaches it — a second name for the same variable — must
// land on that one cell rather than forking a copy.
func TestGoSourceCaptureClonerAliasesSharedCell(t *testing.T) {
	shared, copied := scalarCell("shared"), scalarCell("copied")
	scope := newBashPPScope(nil)
	scope.entries["shared"] = shared
	scope.entries["alias"] = shared
	scope.entries["copied"] = copied

	cloner := newBashPPCloner()
	cloner.shared = map[*bashPPCell]bool{shared: true}
	out := cloner.clone(scope)

	if out.entries["shared"] != shared {
		t.Error("a captured cell must survive the clone as the same cell")
	}
	if out.entries["alias"] != shared {
		t.Error("two names for one captured variable must stay one cell")
	}
	if out.entries["copied"] == copied {
		t.Error("an uncaptured cell must still be deep copied")
	}
	if out.entries["copied"].vr.Str != "copied" {
		t.Errorf("copied cell lost its value: %q", out.entries["copied"].vr.Str)
	}
}

// TestGoSourceCaptureTaskCellsSkipsShared covers the second hook. The task
// walk reaches a shared cell through the child's scope, and copying its
// payload there would rewrite the PARENT's variable in place — the one thing
// sharing must never do.
func TestGoSourceCaptureTaskCellsSkipsShared(t *testing.T) {
	shared := &bashPPCell{vr: expand.Variable{Kind: expand.Object, Obj: map[string]any{"n": "1"}}}
	child := &Runner{bashPPScope: newBashPPScope(nil)}
	child.bashPPScope.entries["shared"] = shared
	before := shared.vr.Obj

	if err := cloneBashPPTaskCells(child, newBashPPObjectCloner(), map[*bashPPCell]bool{shared: true}); err != nil {
		t.Fatal(err)
	}
	if !sameObject(shared.vr.Obj, before) {
		t.Error("the task walk replaced a shared cell's payload, which is the parent's")
	}
}

func sameObject(a, b any) bool {
	am, aok := a.(map[string]any)
	bm, bok := b.(map[string]any)
	if !aok || !bok {
		return false
	}
	if len(am) != len(bm) {
		return false
	}
	// Two map values are the same map exactly when writing one is visible in
	// the other; comparing contents would pass for a copy too.
	am["probe"] = "x"
	same := bm["probe"] == "x"
	delete(am, "probe")
	return same
}

// The precision cases below are the ones a metadata-only, collect-every-Lit
// analysis got wrong. Each names a cell that must NOT be shared, or one that
// must be, and none of them is decidable without lexical scope.

// TestGoSourceCaptureStringLiteralIsNotAUse: the text inside a string literal
// is data. Sharing an unrelated outer cell because a printf argument spelled
// its name splices a live parent variable into a running task for a variable
// the program never captured.
func TestGoSourceCaptureStringLiteralIsNotAUse(t *testing.T) {
	r, cells := captureRunnerFor("counter", "total")
	g := parseGoStmt(t, "func main() {\n\tgo func() {\n\t\tfmt.Println(\"total\", 'total', counter)\n\t}()\n}\n")
	shared, _ := r.bashPPGoSourceTaskCapture(g.Call)
	if shared[cells["total"]] {
		t.Error("string literal text is not a variable use")
	}
	if !shared[cells["counter"]] {
		t.Error("the real free variable was not captured")
	}
}

// TestGoSourceCaptureLocalShadowsOuter: a body that declares its own `x` never
// names the outer `x`, so the outer cell keeps copy semantics.
func TestGoSourceCaptureLocalShadowsOuter(t *testing.T) {
	for name, src := range map[string]string{
		"short_decl": "func main() {\n\tgo func() {\n\t\tx := 1\n\t\tx++\n\t}()\n}\n",
		"var_decl":   "func main() {\n\tgo func() {\n\t\tvar x int\n\t\tx++\n\t}()\n}\n",
		"range_bind": "func main() {\n\tgo func() {\n\t\tfor x := range ch {\n\t\t\tuse(x)\n\t\t}\n\t}()\n}\n",
	} {
		t.Run(name, func(t *testing.T) {
			r, cells := captureRunnerFor("x")
			g := parseGoStmt(t, src)
			if shared, _ := r.bashPPGoSourceTaskCapture(g.Call); shared[cells["x"]] {
				t.Error("a local binding must not grant identity to the outer cell it shadows")
			}
		})
	}
}

// TestGoSourceCaptureUnicodeIdentifier: Go identifiers are Unicode, and an
// ASCII-only predicate silently dropped these from the capture set, so a
// correctly synchronized program using them printed the parent's stale copy.
func TestGoSourceCaptureUnicodeIdentifier(t *testing.T) {
	r, cells := captureRunnerFor("计数器", "naïve")
	g := parseGoStmt(t, "func main() {\n\tgo func() {\n\t\t计数器++\n\t\tnaïve++\n\t}()\n}\n")
	shared, _ := r.bashPPGoSourceTaskCapture(g.Call)
	for _, name := range []string{"计数器", "naïve"} {
		if !shared[cells[name]] {
			t.Errorf("Unicode identifier %q was not captured", name)
		}
	}
}

// TestGoSourceCaptureNestedClosure: a variable free in an inner closure is
// free in the body that encloses it — the outer body is what must capture it
// so the inner one can — while the inner closure's own parameter still
// shadows.
func TestGoSourceCaptureNestedClosure(t *testing.T) {
	r, cells := captureRunnerFor("counter", "p")
	g := parseGoStmt(t, "func main() {\n\tgo func() {\n\t\tinner := func(p int) {\n\t\t\tcounter++\n\t\t\tp++\n\t\t}\n\t\tinner(1)\n\t}()\n}\n")
	shared, _ := r.bashPPGoSourceTaskCapture(g.Call)
	if !shared[cells["counter"]] {
		t.Error("a nested closure's free variable is free in the launched body")
	}
	if shared[cells["p"]] {
		t.Error("a nested closure's parameter shadows the outer spelling")
	}
}

// TestGoSourceCaptureClosureHeldInVariable: `go f()` where f is a closure in a
// variable used to return nil and silently fall back to the deep copy, so the
// body's captures were copied and the program printed a stale parent value.
// The callee resolves here, once, and the pin keeps the child on it.
func TestGoSourceCaptureClosureHeldInVariable(t *testing.T) {
	r, cells := captureRunnerFor("counter")
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(
		strings.NewReader("func main() {\n\tf := func() {\n\t\tcounter++\n\t}\n\tgo f()\n}\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	var lit *syntax.BashPPFuncLit
	var g *syntax.BashPPGo
	syntax.Walk(file, func(node syntax.Node) bool {
		switch node := node.(type) {
		case *syntax.BashPPFuncLit:
			if lit == nil {
				lit = node
			}
		case *syntax.BashPPGo:
			if g == nil {
				g = node
			}
		}
		return true
	})
	if lit == nil || g == nil {
		t.Fatal("expected a literal and a go statement")
	}
	fn := &bashPPFunc{lit: lit, scope: r.bashPPScope}
	r.bashPPClosures = append(r.bashPPClosures, fn)
	handle := bashPPFuncHandlePrefix + "0"
	r.bashPPScope.entries["f"] = scalarCell(handle)

	shared, pin := r.bashPPGoSourceTaskCapture(g.Call)
	if !shared[cells["counter"]] {
		t.Error("a closure held in a variable must capture its free variables")
	}
	if pin == nil || pin.call != g.Call || pin.handle != handle {
		t.Errorf("the launched callee was not pinned: %+v", pin)
	}
}

// TestGoSourceCaptureInexactSharesNothing is the fail-closed contract. A
// construct the walker does not model must cost sharing, never soundness: the
// task falls back to the classic deep-copy snapshot rather than aliasing cells
// the analysis could not account for.
func TestGoSourceCaptureInexactSharesNothing(t *testing.T) {
	r, _ := captureRunnerFor("counter")
	g := parseGoStmt(t, "func main() {\n\tgo func() {\n\t\tcounter++\n\t\techo \"$unmodelled\"\n\t}()\n}\n")
	if shared, _ := r.bashPPGoSourceTaskCapture(g.Call); len(shared) != 0 {
		t.Errorf("an inexact analysis must share nothing, got %d cells", len(shared))
	}
}
