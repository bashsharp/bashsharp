// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

// Sprint: #118; Story: #54; Story-ID: c3a60493cde9
//
// Review corrections to the GoSource task-capture analysis: the refusal is
// explicit, the classification answers from the declared type, and the payload
// walk no longer grants identity by running out of depth.

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// TestGoSourceCaptureUnsupportedIsDiagnosed is the correction this file exists
// for. An unmodelled construct used to return a nil capture set, which is the
// classic deep-copy snapshot — a DIFFERENT program, not a conservative one:
// the body gets a private copy of a variable Go says it shares. The launch is
// now refused with a diagnostic naming the reason, and the refusal is visible
// on the runner rather than only in the returned set.
func TestGoSourceCaptureUnsupportedIsDiagnosed(t *testing.T) {
	r, cells := captureRunnerFor("counter")
	// A shell command line is not a Go construct, which is one of the shapes
	// the scope walker declines to model.
	g := parseGoStmt(t, "func main() {\n\tgo func() {\n\t\techo hello\n\t\tcounter++\n\t}()\n}\n")
	shared, _ := r.bashPPGoSourceTaskCapture(g.Call)
	if shared[cells["counter"]] {
		t.Fatal("an inexact analysis must not share anything")
	}
	if r.exit.err == nil {
		t.Fatal("an unsupported capture analysis must be diagnosed, not silently deep copied")
	}
	if got := r.exit.err.Error(); !strings.Contains(got, "unsupported task capture") {
		t.Fatalf("diagnostic does not name the refusal: %q", got)
	}
}

// TestGoSourceCaptureUnresolvedCalleeIsDiagnosed covers the other silent path:
// a launched callee that resolves to no original body at all. It cannot be
// given Go's by-reference meaning either, so it is refused for the same reason.
//
// The name here is bound, and bound to something that is not a function, which
// is also the shape that proves the lookup stops at the innermost binding
// rather than falling through to a declared func.
func TestGoSourceCaptureUnresolvedCalleeIsDiagnosed(t *testing.T) {
	r, _ := captureRunnerFor("counter", "notAFunc")
	call := &syntax.BashPPCall{Fun: []*syntax.Lit{{Value: "notAFunc"}}}
	if shared, _ := r.bashPPGoSourceTaskCapture(call); shared != nil {
		t.Fatalf("an unresolved callee must share nothing, got %d cells", len(shared))
	}
	if r.exit.err == nil {
		t.Fatal("an unresolvable launched callee must be diagnosed")
	}
	if got := r.exit.err.Error(); !strings.Contains(got, "unsupported task capture") {
		t.Fatalf("diagnostic does not name the refusal: %q", got)
	}
}

// TestGoSourceCaptureExactEmptyIsNotRefused keeps the refusal narrow. A body
// that captures nothing is fully understood; there is simply nothing to share,
// and that is not an error.
func TestGoSourceCaptureExactEmptyIsNotRefused(t *testing.T) {
	r, _ := captureRunnerFor("counter")
	g := parseGoStmt(t, "func main() {\n\tgo func() {\n\t\tvar n int\n\t\tn++\n\t}()\n}\n")
	if shared, _ := r.bashPPGoSourceTaskCapture(g.Call); shared != nil {
		t.Fatalf("a body that captures nothing must share nothing, got %d cells", len(shared))
	}
	if r.exit.err != nil {
		t.Fatalf("an exact, empty capture set is not a refusal: %v", r.exit.err)
	}
}

// TestGoSourceSharableAnswersFromDeclaredType pins the immutable authority.
//
// A cell's declared type is fixed by Go at the declaration and no concurrent
// writer can change it, which is what makes it safe to consult while another
// goroutine holds the variable. A package-qualified name is a dependency type,
// and identity for those stays bashpp_task.go's decision.
func TestGoSourceSharableAnswersFromDeclaredType(t *testing.T) {
	named := func(name string) syntax.BashPPTypeExpr {
		return &syntax.BashPPNamedType{Name: &syntax.Lit{Value: name}}
	}
	tests := []struct {
		name string
		typ  syntax.BashPPTypeExpr
		want bool
	}{
		{"plain scalar", named("int"), true},
		{"dependency value", named("sync.Mutex"), false},
		{"pointer to dependency value", &syntax.BashPPPointerType{Element: named("sync.WaitGroup")}, false},
		{"slice of scalars", &syntax.BashPPCollectionType{Kind: "slice", Element: named("string")}, true},
		{"slice of dependency values", &syntax.BashPPCollectionType{Kind: "slice", Element: named("os.File")}, false},
		{"map to dependency values", &syntax.BashPPCollectionType{Kind: "map", Key: named("string"), Element: named("atomic.Int64")}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r := &Runner{bashPPGoSource: true, bashPPScope: newBashPPScope(nil)}
			// A payload that would answer the OPPOSITE way if it were read.
			// The declared type must win, and must win without reading it.
			cell := &bashPPCell{declType: test.typ}
			if test.want {
				cell.vr = expand.Variable{Kind: expand.Object, Obj: map[string]any{"h": &bashPPBridgeValue{Kind: "handle"}}}
			}
			if got := r.bashPPGoSourceSharable(cell); got != test.want {
				t.Fatalf("declared type %T answered %v, want %v", test.typ, got, test.want)
			}
		})
	}
}

// TestGoSourceCellHoldsNativeIsTotal is the depth correction.
//
// The previous walk stopped at depth 8 and reported "no native handle here",
// which GRANTS identity to everything below the cut: a handle nested deeper
// than the cap would have been aliased as if it were a plain value. The walk is
// now total, bounded by a visited set rather than a depth budget.
func TestGoSourceCellHoldsNativeIsTotal(t *testing.T) {
	deep := any(&bashPPBridgeValue{Kind: "handle", Session: "s", Handle: 1})
	for range 24 {
		deep = map[string]any{"next": deep}
	}
	if !bashPPCellHoldsNative(&bashPPCell{vr: expand.Variable{Kind: expand.Object, Obj: deep}}) {
		t.Fatal("a native handle below the old depth cap must still be found")
	}
}

// TestGoSourceCellHoldsNativeTerminatesOnCycles is the other half of dropping
// the depth cap: without a budget, a payload that refers to itself must still
// terminate.
func TestGoSourceCellHoldsNativeTerminatesOnCycles(t *testing.T) {
	loop := map[string]any{"plain": "0"}
	loop["self"] = loop
	if bashPPCellHoldsNative(&bashPPCell{vr: expand.Variable{Kind: expand.Object, Obj: loop}}) {
		t.Fatal("a cyclic plain payload holds no native handle")
	}
	loop["handle"] = &bashPPBridgeValue{Kind: "handle"}
	if !bashPPCellHoldsNative(&bashPPCell{vr: expand.Variable{Kind: expand.Object, Obj: loop}}) {
		t.Fatal("a cyclic payload that does hold a handle must still report it")
	}
}

// TestGoSourceCellHoldsNativeFailsClosed pins the direction of the unknown
// case. A payload shape this walk does not model is reported as native so the
// cell is copied under the reviewed bashpp_task.go rule, rather than aliased on
// an assumption about a value nobody classified.
func TestGoSourceCellHoldsNativeFailsClosed(t *testing.T) {
	type unmodelled struct{ X int }
	if !bashPPCellHoldsNative(&bashPPCell{vr: expand.Variable{Kind: expand.Object, Obj: &unmodelled{}}}) {
		t.Fatal("an unmodelled payload must fail closed")
	}
}

// TestGoSourceCaptureLocalBindingShadowsDeclaredFunc is the lookup correction.
//
// Go resolves `go f()` to the innermost binding of f. The analysis consulted
// the package-level func table first, so it walked a body that was never
// launched — and, because the pin comes from the same resolution, could have
// launched one function while sharing another's capture set.
func TestGoSourceCaptureLocalBindingShadowsDeclaredFunc(t *testing.T) {
	r, cells := captureRunnerFor("counter")
	global := &syntax.BashPPFuncDecl{
		Name: &syntax.Lit{Value: "f"},
		Body: mustParseBlock(t, "global"),
	}
	r.bashPPFuncs = map[string]*bashPPFunc{"f": {decl: global, scope: r.bashPPScope}}

	_, value := r.bashPPMakeClosure(mustParseFuncLit(t, "func main() {\n\tf := func() {\n\t\tcounter++\n\t}\n}\n"))
	handle := value.Str
	r.bashPPScope.entries["f"] = &bashPPCell{vr: value}

	shared, pin := r.bashPPGoSourceTaskCapture(&syntax.BashPPCall{Fun: []*syntax.Lit{{Value: "f"}}})
	if r.exit.err != nil {
		t.Fatalf("the local binding is resolvable: %v", r.exit.err)
	}
	if !shared[cells["counter"]] {
		t.Fatal("the local closure's capture set was not used; the declared func shadowed it")
	}
	if pin == nil || pin.handle != handle {
		t.Fatalf("the launch must pin the local closure, got %+v", pin)
	}
}

func mustParseBlock(t *testing.T, name string) *syntax.Block {
	t.Helper()
	lit := mustParseFuncLit(t, "func main() {\n\tg := func() {\n\t\t"+name+"++\n\t}\n}\n")
	return lit.Body
}

func mustParseFuncLit(t *testing.T, src string) *syntax.BashPPFuncLit {
	t.Helper()
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	var found *syntax.BashPPFuncLit
	syntax.Walk(file, func(node syntax.Node) bool {
		if lit, ok := node.(*syntax.BashPPFuncLit); ok && found == nil {
			found = lit
		}
		return true
	})
	if found == nil {
		t.Fatalf("no func literal in %q", src)
	}
	return found
}

// TestGoSourceSharableRefusesUnresolvableNamedType is the type-spelling
// correction, as revised by the combined capture+WaitGroup regression
// (Sprint #118, Story #54).
//
// The spelling half stands unchanged: the classifier answered "plain,
// decided" for EVERY unqualified named type, which reads identity off the
// spelling rather than off the value. An unresolvable name must report
// UNDECIDED so the total, fail-closed payload walk answers instead.
//
// The payload half was corrected. The original revision made the payload walk
// copy any composite that held a native field descriptor, to keep
// bashpp_task.go's descriptor-copy rule the sole authority on native
// identity. That protected the one thing which needed no protection — the
// descriptor, immutable once installed — while copying away the ORIGINAL
// mutable map/slice fields beside it: a Container{mu sync.Mutex; counters
// map[string]int} captured through a closure was deep copied per task, so
// three correctly synchronized goroutines printed the parent's untouched
// map[a:0 b:0] where Go prints map[a:20000 b:10000]. A local struct is ONE
// original Go variable; sharing it keeps the field references and names the
// same session objects from either side. So a composite payload — handle
// fields included — is now shared, while a payload that IS a native handle
// keeps the descriptor-copy rule, and unmodelled shapes still fail closed.
func TestGoSourceSharableRefusesUnresolvableNamedType(t *testing.T) {
	named := func(name string) syntax.BashPPTypeExpr {
		return &syntax.BashPPNamedType{Name: &syntax.Lit{Value: name}}
	}
	native := func() expand.Variable {
		return expand.Variable{Kind: expand.Object, Obj: map[string]any{
			"mu": &bashPPBridgeValue{Kind: "handle", Type: "sync.Mutex"},
		}}
	}
	direct := func() expand.Variable {
		return expand.Variable{Kind: expand.Object,
			Obj: &bashPPBridgeValue{Kind: "handle", Type: "sync.Mutex", Session: "s", Handle: 1}}
	}
	plain := func() expand.Variable {
		return expand.Variable{Kind: expand.Object, Obj: map[string]any{"n": 1}}
	}
	tests := []struct {
		name    string
		typ     syntax.BashPPTypeExpr
		payload func() expand.Variable
		want    bool
	}{
		// Corrected: a local struct embedding a handle is one original Go
		// variable and is shared WHOLE, so its original map/slice fields keep
		// their reference identity across the task boundary.
		{"local struct embedding a handle", named("counter"), native, true},
		{"interface spelling holding a composite", named("any"), native, true},
		{"error spelling holding a composite", named("error"), native, true},
		{"slice of local structs embedding handles",
			&syntax.BashPPCollectionType{Kind: "slice", Element: named("counter")}, native, true},
		// The spelling still decides nothing by itself: a payload that IS a
		// native handle keeps bashpp_task.go's descriptor-copy rule.
		{"interface spelling holding a direct handle", named("any"), direct, false},
		{"error spelling holding a direct handle", named("error"), direct, false},
		// No regression: an unresolvable name whose payload really is plain is
		// still shared, because the payload walk — not the spelling — decides.
		{"local struct with no handle", named("point"), plain, true},
		{"predeclared scalar", named("int"), plain, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r := &Runner{bashPPGoSource: true, bashPPScope: newBashPPScope(nil)}
			cell := &bashPPCell{declType: test.typ, vr: test.payload()}
			if got := r.bashPPGoSourceSharable(cell); got != test.want {
				t.Fatalf("sharable(%s) = %v, want %v", test.name, got, test.want)
			}
		})
	}
}

// TestGoSourceSharableClassificationIsImmutableAndInherited pins the two
// properties the race proof rests on: a cell's answer is decided ONCE, and a
// nested launch inherits it rather than re-reading a payload a running task may
// concurrently be writing.
func TestGoSourceSharableClassificationIsImmutableAndInherited(t *testing.T) {
	r := &Runner{bashPPGoSource: true, bashPPScope: newBashPPScope(nil)}
	cell := &bashPPCell{
		declType: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "point"}},
		vr:       expand.Variable{Kind: expand.Object, Obj: map[string]any{"n": 1}},
	}
	if !r.bashPPGoSourceSharable(cell) {
		t.Fatal("a plain payload under an unresolvable name must be sharable")
	}
	// Mutate the payload the way a running task would. The memoized answer must
	// not change: re-inspecting here is the data race the memo exists to avoid.
	// The mutation is to a DIRECT native handle — a shape the fresh-answer
	// path would refuse — so this still discriminates: only the memo keeps the
	// answer true once a composite-holding struct is legitimately shared.
	cell.vr = expand.Variable{Kind: expand.Object,
		Obj: &bashPPBridgeValue{Kind: "handle", Type: "sync.Mutex", Session: "s", Handle: 2}}
	if !r.bashPPGoSourceSharable(cell) {
		t.Fatal("the classification must be immutable once decided")
	}
	// A nested launch clones the memo and must answer from it, without reading.
	child := &Runner{bashPPGoSource: true, bashPPScope: newBashPPScope(nil)}
	child.bashPPGoSourceSharableCells = make(map[*bashPPCell]bool, len(r.bashPPGoSourceSharableCells))
	for k, v := range r.bashPPGoSourceSharableCells {
		child.bashPPGoSourceSharableCells[k] = v
	}
	if !child.bashPPGoSourceSharable(cell) {
		t.Fatal("a nested launch must inherit the parent's classification")
	}
}
