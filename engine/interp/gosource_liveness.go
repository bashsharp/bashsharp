package interp

// Sprint: #248; Story: #424; Story-ID: ffabc6c1c44a

import (
	"reflect"
	"sync"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// SOURCE-LEVEL VARIABLE LIFETIME.
//
// A Go local stops keeping its value reachable after its last use, not at the
// end of its block: gc's liveness analysis is what lets a finalizer run while
// the variable is still in scope (fixedbugs/issue46725.go asserts exactly
// that). The interpreter binds a block's variables in its scope map, so the
// binding itself would keep the value reachable until the block is left.
//
// goSourceLiveness computes, once per statement list, which variables that
// list declares are never named again after statement i, and the executing
// list drops those bindings once statement i completes. Only the binding is
// dropped — the cell is left intact — so storage that is still aliased
// through a pointer, a slice or a captured closure stays exactly as it was.
//
// The analysis is deliberately conservative; every doubt keeps the binding:
//   - a name counts as used by any identifier-shaped token anywhere in a later
//     statement, including shadowing declarations and field names;
//   - a function literal names a variable where it is written: a closure
//     binds the variables visible at its creation in a scope of its own
//     (bashPPScope.snapshot) sharing the cells, so it never resolves a name
//     through the frame's binding after the literal was evaluated;
//   - a list containing a label or goto is never analysed, because control
//     can re-enter an earlier statement;
//   - only `var` and `:=` bindings declared by the list itself are released,
//     and only from the scope that was current when the list began.

type goSourceLivenessPlan struct {
	release [][]string
}

type goSourceLivenessKey struct {
	first *syntax.Stmt
	n     int
}

var goSourceLivenessCache sync.Map // goSourceLivenessKey -> *goSourceLivenessPlan

// goSourceLivenessFor reports the release plan of stmts, or nil when the list
// declares nothing it can release.
func goSourceLivenessFor(stmts []*syntax.Stmt) *goSourceLivenessPlan {
	if len(stmts) == 0 {
		return nil
	}
	key := goSourceLivenessKey{first: stmts[0], n: len(stmts)}
	if cached, ok := goSourceLivenessCache.Load(key); ok {
		return cached.(*goSourceLivenessPlan)
	}
	plan := computeGoSourceLiveness(stmts)
	goSourceLivenessCache.Store(key, plan)
	return plan
}

func computeGoSourceLiveness(stmts []*syntax.Stmt) *goSourceLivenessPlan {
	names := make([]map[string]bool, len(stmts))
	for i, stmt := range stmts {
		w := goSourceNameWalker{names: map[string]bool{}, seen: map[uintptr]bool{}}
		if !w.walk(reflect.ValueOf(stmt)) {
			return nil
		}
		names[i] = w.names
	}
	var release [][]string
	for i, stmt := range stmts {
		for _, name := range goSourceDeclaredNames(stmt) {
			// The blank identifier binds a discarded cell no statement can
			// read; it is dead as soon as its declaration completes.
			last := i
			for j := i + 1; j < len(stmts) && name != "_"; j++ {
				if names[j][name] {
					last = j
				}
			}
			if release == nil {
				release = make([][]string, len(stmts))
			}
			release[last] = append(release[last], name)
		}
	}
	if release == nil {
		return nil
	}
	return &goSourceLivenessPlan{release: release}
}

// goSourceDeclaredNames reports the variables a statement binds in the block
// that executes it.
func goSourceDeclaredNames(stmt *syntax.Stmt) []string {
	if stmt == nil {
		return nil
	}
	switch x := stmt.Cmd.(type) {
	case *syntax.BashPPShortDecl:
		var out []string
		for _, lit := range x.Lhs {
			if lit != nil {
				out = append(out, lit.Value)
			}
		}
		return out
	case *syntax.BashPPDecl:
		if x.Site == syntax.StartVar && x.Name != nil {
			return []string{x.Name.Value}
		}
	}
	return nil
}

var goSourceSyntaxPkg = reflect.TypeFor[syntax.Stmt]().PkgPath()

// goSourceNameWalker collects every identifier-shaped token in a syntax
// subtree. It reports false for a subtree that transfers control by label.
type goSourceNameWalker struct {
	names map[string]bool
	seen  map[uintptr]bool
}

var (
	goSourceLabeledType = reflect.TypeFor[syntax.BashPPLabeled]()
	goSourceGotoType    = reflect.TypeFor[syntax.BashPPGoto]()
)

func (w *goSourceNameWalker) walk(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() || v.Type().Elem().PkgPath() != goSourceSyntaxPkg {
			return true
		}
		if w.seen[v.Pointer()] {
			return true
		}
		w.seen[v.Pointer()] = true
		switch v.Type().Elem() {
		case goSourceLabeledType, goSourceGotoType:
			return false
		}
		return w.walk(v.Elem())
	case reflect.Interface:
		if v.IsNil() {
			return true
		}
		return w.walk(v.Elem())
	case reflect.Struct:
		if v.Type().PkgPath() != goSourceSyntaxPkg {
			return true
		}
		for i := range v.NumField() {
			if !w.walk(v.Field(i)) {
				return false
			}
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			if !w.walk(v.Index(i)) {
				return false
			}
		}
	case reflect.String:
		w.tokens(v.String())
	}
	return true
}

func (w *goSourceNameWalker) tokens(text string) {
	start := -1
	flush := func(end int) {
		if start >= 0 {
			name := text[start:end]
			w.names[name] = true
			start = -1
		}
	}
	for i := 0; i < len(text); i++ {
		c := text[i]
		ident := c == '_' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || c >= 0x80
		digit := '0' <= c && c <= '9'
		switch {
		case ident || digit && start >= 0:
			if start < 0 {
				start = i
			}
		default:
			flush(i)
		}
	}
	flush(len(text))
}

// goSourceReleaseDead drops, after statement i of a function-body list, the
// bindings the list declared that no later statement names. The results of
// the last call the statement consumed are dead too: they are handed from a
// callee to the expression that consumes them within one statement, and a
// later call only saves and restores them around its own evaluation, which
// would keep a consumed value reachable for no reader. A statement that
// returns or panics leaves them to the frame's own result handling.
func (r *Runner) goSourceReleaseDead(plan *goSourceLivenessPlan, scope *bashPPScope, i int) {
	if r.exit.returning || r.exit.exiting || r.bashPPReturn.active || r.bashPPPanicking() {
		return
	}
	r.bashPPResultCells = nil
	if plan == nil || scope == nil || r.bashPPScope != scope || i >= len(plan.release) {
		return
	}
	for _, name := range plan.release[i] {
		// A `:=` that rebinds one of the frame's named results reuses the
		// result's binding rather than declaring a fresh local, so the release
		// plan — which reads only the statement list's own tokens — must not
		// drop it. A named result is read by every `return`, bare or not, so it
		// stays live until the frame returns. The guard is confined to the
		// block the results live in: a nested block that merely shadows a
		// result's name with a genuinely new local still releases that local.
		if r.goSourceFrameNamedResult(scope, name) {
			continue
		}
		delete(scope.entries, name)
	}
}

// goSourceFrameNamedResult reports whether name is a named result of the
// function whose body block is scope, so that source-level liveness leaves the
// result's binding in place until the frame returns.
func (r *Runner) goSourceFrameNamedResult(scope *bashPPScope, name string) bool {
	if len(r.callStack) == 0 {
		return false
	}
	frame := &r.callStack[len(r.callStack)-1]
	if frame.frameScope != scope || frame.bashPPFn == nil {
		return false
	}
	for _, result := range bashppResultNames(frame.bashPPFn.bodyResults()) {
		if result == name {
			return true
		}
	}
	return false
}

// goSourceClosureScope keeps the bindings a source closure can name. A full
// snapshot would make the closure retain unrelated dead locals of every
// enclosing frame. The name walk is conservative (including nested literals
// and type/field names); unsupported control flow keeps the full snapshot.
// narrowed preserves cell identity, so assignments and escaping aliases still
// observe the original storage. Package initialization uses its live scope in
// bashPPNewClosure because later package bindings must remain visible.
func (r *Runner) goSourceClosureScope(lit *syntax.BashPPFuncLit) *bashPPScope {
	w := goSourceNameWalker{names: map[string]bool{}, seen: map[uintptr]bool{}}
	if !w.walk(reflect.ValueOf(lit)) {
		return r.bashPPScope.snapshot()
	}
	return r.bashPPScope.narrowed(w.names)
}
