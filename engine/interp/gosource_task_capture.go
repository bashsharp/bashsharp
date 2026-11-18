// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// GoSource lexical capture identity across tasks.
//
// A Bash++ task snapshot deep copies every mutable value reachable from the
// shell environment (see [bashPPObjectCloner]); the parent keeps running
// concurrently, and sharing interpreter heap between the two would be a data
// race. That is the right default for classic Bash++, where `go f()` is a
// shell construct and a task is a private copy of the shell.
//
// It is the wrong default for an ORIGINAL Go program. Go closures capture
// their free variables BY REFERENCE, so a goroutine and its parent name the
// same variable:
//
//	var counter int
//	var mu sync.Mutex
//	for i := 0; i < 8; i++ {
//	    wg.Add(1)
//	    go func() {
//	        mu.Lock()
//	        counter++      // must increment the counter the parent prints
//	        mu.Unlock()
//	        wg.Done()
//	    }()
//	}
//	wg.Wait()
//	fmt.Println(counter)   // 8 in Go; 0 under a deep-copy snapshot
//
// bashpp_task.go already carves out imported native handles, so a
// `sync.Mutex` or `atomic.Int64` names one object across the boundary. But the
// plain interpreted cell the mutex PROTECTS was still deep copied, so a
// correctly synchronized original Go program printed the parent's untouched
// copy. This file closes that gap for GoSource programs only.
//
// # The rule
//
// Identity is granted to exactly the cells a task's closure captures
// lexically, and to nothing else:
//
//   - Only in GoSource mode. Classic Bash++ `go f()` keeps its deep-copy
//     snapshot unchanged; [Runner.bashPPGoSourceTaskCapture] returns nil there
//     and every hook below is a no-op on a nil set.
//   - Only names the body actually USES as variables, computed by the lexical
//     scope walker in this file, and in each original callable descriptor
//     carried into the task. Each descriptor retains its lexical references
//     independently of the variable holding its function value. A name the body
//     re-declares — a `:=`, a `var`, a nested parameter, a range or select
//     binding — is a different variable from the outer one that happens to
//     share a spelling, and the outer cell is NOT shared. Text inside a
//     string literal is not a variable use at all.
//   - Native-handle and channel VARIABLES retain their cell identity after
//     first-admission session or task-group authentication. Each operation
//     still validates the referenced object. Classic snapshots still copy
//     descriptors. A local struct holding native fields is also one original
//     variable; copying it would fork mutable map/slice fields beside them.
//
// # Why precision, not over-approximation
//
// An earlier revision collected every [syntax.Lit] the body mentioned and
// argued that over-approximating was "safe because a superset of the free
// variables still resolves". It is not safe. Sharing is not a no-op on a cell
// the task never touches: it removes the deep copy, so the PARENT's live cell
// is spliced into a concurrently running task's environment, where an alias, a
// pointer target or a captured closure scope can reach and mutate it — for a
// variable the program never asked to share. `fmt.Println("counter")` would
// have shared an unrelated outer `counter`, and a body whose own `x := …`
// shadows an outer `x` would have shared the outer one it can never name. The
// walker below therefore reports EXACTNESS for each body, and an inexact
// analysis shares nothing. A task also carries original callable descriptors;
// each keeps its own exact lexical references, even if this particular task
// never invokes it. This is closure identity, not interpreting a string or a
// shadowed local as an outer use:
//
//   - A construct the walker does not model returns exact=false, and the
//     launch is REFUSED with a diagnostic (see
//     [Runner.bashPPGoSourceCaptureUnsupported]). An earlier revision fell
//     back to the classic deep-copy snapshot here; that is not a conservative
//     fallback for an original Go program but a different program, so the gap
//     is now reported rather than run. Either way the walker never silently
//     aliases a cell the program did not name.
//
// # Race safety
//
// A shared cell is shared exactly as a Go variable is, which makes
// synchronization the program's responsibility, as in Go. That is not a
// loophole in practice: every native `mu.Lock()`, `wg.Wait()` and
// `atomic.Add` is a request that serializes on the dependency session's own
// Go mutexes (see [bashPPNativeSession.request]), so a program which
// synchronizes its shared variable the way Go requires also establishes the
// Go happens-before edges the race detector checks. A program which does NOT
// synchronize races here because it races in Go too — reproducing the
// original's meaning includes reproducing that.

// bashPPGoSourcePin pins the function value a launched task resolved to.
//
// `go f()` where f is a variable holding a closure must name ONE function: the
// value f held when the `go` statement ran. Go evaluates the function value in
// the launching goroutine, and so do we — once, in the parent, before the
// snapshot, so the capture analysis and the task itself agree on which body is
// running and a computed callee such as `go factory()()` is never evaluated a
// second time in the child.
//
// The pin carries the closure HANDLE rather than the parent's *bashPPFunc, so
// the child resolves it through its OWN cloned closure registry: same function,
// the child's copy of everything the snapshot legitimately copied.
type bashPPGoSourcePin struct {
	call   *syntax.BashPPCall
	handle string
	bound  *bashPPFunc // ephemeral original method; never added to the parent registry
}

// bashPPGoSourceTaskCapture is the set of cells a launched GoSource task must
// share with its parent rather than copy, plus the callee pin to install on the
// child. A nil set means "copy everything", which is the classic Bash++
// behavior.
func (r *Runner) bashPPGoSourceTaskCapture(call *syntax.BashPPCall) (map[*bashPPCell]bool, *bashPPGoSourcePin) {
	if r == nil || !r.bashPPGoSource || call == nil {
		return nil, nil
	}
	body, params, env, pin := r.bashPPGoSourceTaskBody(call)
	if r.exit.code != 0 || r.exit.err != nil {
		return nil, pin
	}
	if body == nil || env == nil {
		r.bashPPGoSourceCaptureUnsupported(call, "the launched callee does not resolve to an original function body")
		return nil, pin
	}
	shared, ok := r.bashPPGoSourceCaptureSet(call, body, params, env)
	if !ok {
		// A refused analysis has already reported its diagnostic.
		return nil, pin
	}
	if len(shared) == 0 {
		return nil, pin
	}
	return shared, pin
}

// bashPPGoSourceCaptureSet retains the original free-cell identity of the
// launched body and the original callable descriptors copied into its registry.
// A function value keeps its lexical references wherever that value is stored:
// assigning f after a synchronized first launch must not leave a second task's
// registry pointing at a deep copy of the new closure's environment.
//
// Inspect immutable function syntax and scope bindings, never the current
// payload of a function-valued cell. Such a payload can legally be assigned
// while a previously launched body waits before reading it. Memoizing the first
// value is stale; rereading it as an extra launch-time operand is a host race.
// Every carried descriptor retains its exact lexical free cells, even when a
// particular task never invokes that descriptor. Parameters, shadowed locals,
// and names inside string literals still do not nominate outer cells.
func (r *Runner) bashPPGoSourceCaptureSet(call *syntax.BashPPCall, body *syntax.Block, params map[string]bool, env *bashPPScope) (map[*bashPPCell]bool, bool) {
	shared := make(map[*bashPPCell]bool)
	add := func(body *syntax.Block, params map[string]bool, env *bashPPScope) bool {
		free, exact := bashPPGoSourceFreeNames(body, params)
		if !exact {
			r.bashPPGoSourceCaptureUnsupported(call, "a carried original function contains a construct the lexical capture analysis does not model")
			return false
		}
		if env == nil {
			return true
		}
		for name := range free {
			if cell := env.lookup(name); cell != nil && r.bashPPGoSourceSharable(cell) {
				shared[cell] = true
			}
			if r.exit.err != nil {
				return false
			}
		}
		return true
	}
	if !add(body, params, env) {
		return nil, false
	}
	seen := make(map[*bashPPFunc]bool)
	addFunc := func(fn *bashPPFunc) bool {
		if fn == nil || fn.native != nil || seen[fn] {
			return true
		}
		seen[fn] = true
		if fn.decl == nil && fn.lit == nil {
			r.bashPPGoSourceCaptureUnsupported(call, "a carried original function has no inspectable body")
			return false
		}
		fnBody, fnParams := bashPPGoSourceFuncBody(fn.lit, fn.decl)
		if !add(fnBody, fnParams, fn.scope) {
			return false
		}
		if fn.receiver != nil && r.bashPPGoSourceSharable(fn.receiver) {
			shared[fn.receiver] = true
		}
		return r.exit.err == nil
	}
	for _, fn := range r.bashPPClosures {
		if !addFunc(fn) {
			return nil, false
		}
	}
	for _, fn := range r.bashPPFuncs {
		if !addFunc(fn) {
			return nil, false
		}
	}
	for _, methods := range r.bashPPMethods {
		for _, fn := range methods {
			if !addFunc(fn) {
				return nil, false
			}
		}
	}
	return shared, true
}

// bashPPGoSourceCaptureUnsupported refuses a launch the capture analysis
// cannot answer exactly.
//
// The alternative — quietly returning a nil capture set — is the failure this
// review exists to remove. A nil set means the classic deep-copy snapshot, and
// for an ORIGINAL Go program that is not a conservative fallback but a
// different program: the goroutine gets its own copy of a variable Go says it
// shares, so a correctly synchronized original prints the parent's untouched
// value and looks like it merely computed the wrong number. A refusal with a
// diagnostic is a visible, reportable gap; a silent deep copy is a wrong answer
// wearing a green test.
//
// Classic Bash++ is untouched: this is only ever reached in GoSource mode.
func (r *Runner) bashPPGoSourceCaptureUnsupported(call *syntax.BashPPCall, reason string) {
	if r.bashPPPanicking() {
		return
	}
	r.exit.fatal(fmt.Errorf("%sgosource: unsupported task capture: %s; an original Go closure captures its free variables by reference and this launch cannot be given that meaning",
		r.bashErrPrefix(call.Pos()), reason))
}

// bashPPGoSourceTaskBody reports the launched body, its parameter names, the
// lexical environment the body's free variables resolve against, and the pin
// that keeps the child on the same resolved function.
//
// An immediately invoked literal — which is what `go func(){…}()` is — closes
// over the launching scope. A named callee closes over the environment it was
// declared in, which is what its [bashPPFunc] already carries. A closure held
// in a variable, or produced by a computed callee, is RESOLVED here, once.
func (r *Runner) bashPPGoSourceTaskBody(call *syntax.BashPPCall) (*syntax.Block, map[string]bool, *bashPPScope, *bashPPGoSourcePin) {
	if lit := call.FuncLit; lit != nil {
		body, params := bashPPGoSourceFuncBody(lit, nil)
		return body, params, r.bashPPScope, nil
	}
	fn, pin := r.bashPPGoSourceTaskFunc(call)
	if fn == nil {
		return nil, nil, nil, pin
	}
	env := fn.scope
	if env == nil {
		env = r.bashPPScope
	}
	if lit := fn.lit; lit != nil {
		body, params := bashPPGoSourceFuncBody(lit, nil)
		return body, params, env, pin
	}
	if decl := fn.decl; decl != nil {
		body, params := bashPPGoSourceFuncBody(nil, decl)
		return body, params, env, pin
	}
	return nil, nil, nil, pin
}

// bashPPGoSourceTaskFunc resolves a non-literal callee to the exact function
// the task will run, without evaluating anything twice.
//
// A declared `func` and a closure held in a variable are pure lookups. A
// computed callee is the only form that must be EVALUATED, and evaluating it
// here is what Go does — the function value is computed in the launching
// goroutine — so the returned pin, not a second evaluation in the child, is
// what the task calls.
func (r *Runner) bashPPGoSourceTaskFunc(call *syntax.BashPPCall) (*bashPPFunc, *bashPPGoSourcePin) {
	if method, ok := call.CalleeExpr.(*syntax.BashPPSelectorExpr); ok && method.MethodValue && !r.bashPPNativeExpr(method.X) {
		fn, err := r.goSourceLocalMethod(method, len(call.Args) > 0)
		if err != nil {
			r.exit.fatal(err)
			return nil, nil
		}
		return r.goSourceTaskMethodPin(call, fn)
	}
	if call.CalleeExpr != nil {
		cell, err := r.goSourceValueCell(call.CalleeExpr)
		if err != nil {
			r.exit.fatal(err)
			return nil, nil
		}
		// The kind test, the closure the handle names and the handle pinned
		// into the task must all describe ONE store, or the task is pinned to
		// a callable other than the one that was resolved.
		vr := cell.viewVar()
		if vr.Kind != expand.String {
			return nil, nil
		}
		fn, ok := r.bashPPClosure(vr.Str)
		if !ok {
			return nil, nil
		}
		return r.goSourcePinTaskCallable(call, fn, vr.Str)
	}
	if len(call.Fun) != 1 {
		if len(call.Fun) > 1 && r.bashPPScope.lookup(call.Fun[0].Value) != nil {
			var receiver syntax.BashPPExpr = &syntax.BashPPIdent{Name: call.Fun[0]}
			for _, field := range call.Fun[1 : len(call.Fun)-1] {
				receiver = &syntax.BashPPSelectorExpr{X: receiver, Sel: field}
			}
			method := &syntax.BashPPSelectorExpr{X: receiver, Sel: call.Fun[len(call.Fun)-1], MethodValue: true, ReceiverAddressable: true}
			fn, err := r.goSourceLocalMethod(method, len(call.Args) > 0)
			if err != nil {
				r.exit.fatal(err)
				return nil, nil
			}
			return r.goSourceTaskMethodPin(call, fn)
		}
		fn, ok := r.bashPPLookupFunc(call)
		if !ok {
			return nil, nil
		}
		return r.goSourceTaskMethodPin(call, fn)
	}
	name := call.Fun[0].Value
	// A lexical binding is considered BEFORE a package-level `func` of the
	// same name, because that is what Go's scoping says: an inner
	//
	//	f := func() { … }
	//
	// shadows a file-scope `func f()` for the rest of its block, and `go f()`
	// there launches the local closure. Consulting r.bashPPFuncs first made
	// the analysis walk the global body while the pin — and therefore the
	// task — ran the local one, so the capture set was computed from a
	// function that was never launched.
	if cell := r.bashPPScope.lookup(name); cell != nil {
		// As above: one snapshot decides the shadowing binding's kind, the
		// closure it names and the handle the task is pinned to.
		vr := cell.viewVar()
		if vr.Kind != expand.String {
			// The name is bound here to something that is not a function
			// value. It still shadows the declared func, so there is no
			// original body to launch through this analysis.
			return nil, nil
		}
		fn, ok := r.bashPPClosure(vr.Str)
		if !ok {
			return nil, nil
		}
		return r.goSourcePinTaskCallable(call, fn, vr.Str)
	}
	// A closure held in a shell variable: the cell's value is the handle, so
	// the exact function is resolvable without running anything.
	if vr := r.lookupVar(name); vr.Kind == expand.String {
		if fn, ok := r.bashPPClosure(vr.Str); ok {
			return r.goSourcePinTaskCallable(call, fn, vr.Str)
		}
	}
	if fn, ok := r.bashPPFuncs[name]; ok {
		return fn, nil
	}
	return nil, nil
}

// bashPPGoSourceFuncBody reports a function's body and the names its signature
// binds. Parameters and named results are both bindings of the function's own
// scope: they shadow an outer variable of the same spelling, and a task's
// `go func(n int){…}(i)` therefore copies `i` by value the way Go does.
func bashPPGoSourceFuncBody(lit *syntax.BashPPFuncLit, decl *syntax.BashPPFuncDecl) (*syntax.Block, map[string]bool) {
	names := make(map[string]bool)
	switch {
	case lit != nil:
		bashPPGoSourceAddFieldNames(names, lit.Params)
		bashPPGoSourceAddFieldNames(names, lit.Results)
		return lit.Body, names
	case decl != nil:
		bashPPGoSourceAddFieldNames(names, decl.Params)
		bashPPGoSourceAddFieldNames(names, decl.Results)
		if recv := decl.Receiver; recv != nil && recv.Name != nil {
			names[recv.Name.Value] = true
		}
		return decl.Body, names
	}
	return nil, names
}

func bashPPGoSourceAddFieldNames(names map[string]bool, fields []*syntax.BashPPField) {
	for _, field := range fields {
		if field == nil {
			continue
		}
		for _, name := range field.Names {
			if name != nil {
				names[name.Value] = true
			}
		}
	}
}

// bashPPGoSourceSharable decides, ONCE per cell, whether identity may be
// granted to it, and remembers the answer.
//
// The decision must be made before the cell can be shared with any task,
// because after that the parent is no longer the only goroutine touching it:
// re-inspecting the payload on a later launch would read a value a running task
// is concurrently writing, which is a data race in the interpreter itself
// rather than in the program it runs. (`go test -race` on a loop that launches
// the same closure repeatedly reports exactly that.)
//
// # Why a nested task never inspects a shared payload
//
// The memo below is the ownership record, and it is the SUPERSET of everything
// ever shared: a cell is shared only after this function has written an entry
// for it, and [Runner.subshell] hands each task its own clone of the memo (see
// api.go). So inside a task:
//
//   - a cell the parent shared is present in the inherited memo under the SAME
//     pointer, and is answered from the memo without reading the payload;
//   - a cell that is absent from the memo was deep copied into this task, or
//     was declared inside it, and is therefore private to this runner — the
//     only goroutine that can read or write it is this one.
//
// That is the race proof. It holds for arbitrarily nested launches because
// each level clones the level above's memo before its task can run.
//
// The classification itself prefers the cell's DECLARED TYPE, which Go fixes at
// the declaration and which no concurrent writer can change. Payload inspection
// is the fallback for an inferred binding only, and it is reached only on a
// cell this runner privately owns.
func (r *Runner) bashPPGoSourceSharable(cell *bashPPCell) bool {
	if cell == nil {
		return false
	}
	if decided, ok := r.bashPPGoSourceSharableCells[cell]; ok {
		return decided
	}
	decided, handled := false, cell.constant
	if !handled {
		decided, handled = r.goSourceCapturedHandleCell(cell)
	}
	if !handled {
		decided = r.bashPPGoSourceSharableCell(cell)
	}
	if r.bashPPGoSourceSharableCells == nil {
		r.bashPPGoSourceSharableCells = make(map[*bashPPCell]bool)
	}
	r.bashPPGoSourceSharableCells[cell] = decided
	return decided
}

// bashPPGoSourceSharableCell classifies the remaining non-handle payloads.
// Authenticated native/channel variables are admitted by goSourceCapturedHandleCell
// before this fallback; this classifier alone grants no native authority.
//
// This fallback excludes direct native handles and channels because their
// first-admission authentication is separate from plain type classification.
//
// A local struct that HOLDS native fields is the opposite case, and sharing
// it is the corrected rule: the struct is one ORIGINAL Go variable — the
// closure captures it by reference — so the snapshot must not copy it away.
// Copying "to protect the handle" protects only the descriptor, which needs
// no protection: it is immutable once installed (Session/Handle/Type are
// written before a value is ever published), while the copy silently forks
// every ORIGINAL mutable field beside it. That was the combined
// capture+WaitGroup regression: a Container{mu sync.Mutex; counters
// map[string]int} was deep copied per task, so three synchronized
// goroutines printed the parent's untouched map[a:0 b:0] where Go prints
// map[a:20000 b:10000].
//
// The race proof is the same one plain sharing rests on: a shared struct cell
// is touched exactly as a Go variable is, so the program's own
// synchronization — a native mu.Lock/mu.Unlock pair, each a session request
// over real Go mutexes — supplies the happens-before edges the race detector
// checks, and the native objects keep their authenticated session identity
// because the shared payload still names the same Session/Handle.
func (r *Runner) bashPPGoSourceSharableCell(cell *bashPPCell) bool {
	if cell == nil || cell.channel != nil || cell.constant {
		return false
	}
	if plain, decided := r.bashPPGoSourceSharableType(cell.declType, 0); decided {
		return plain
	}
	// An inferred binding has no declared type to answer from. This runner
	// privately owns the cell (see the memo argument above), so reading its
	// payload here races with nobody.
	return bashPPGoSourcePlainPayload(cell.vr.Obj)
}

// bashPPGoSourcePlainPayload decides identity from a payload an undecided
// spelling left open.
//
// Interpreter composites — the struct/array/map/slice layout of an original
// Go value — are shared WHOLE, including fields that hold native descriptors:
// the descriptors name the same session objects from either side, and the
// original mutable fields beside them keep their reference identity. A payload
// that IS a native handle keeps bashpp_task.go's descriptor-copy rule, and an
// unmodelled shape fails closed and is copied rather than aliased.
func bashPPGoSourcePlainPayload(value any) bool {
	switch value.(type) {
	case nil:
		return true
	case string, bool, int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64, float32, float64:
		return true
	case map[string]any, []any:
		return true
	}
	return false
}

// bashPPGoSourceSharableType answers from the declared type alone, reporting
// whether it could answer at all.
//
// A type expression is immutable AST fixed at the declaration, so this answer
// is stable for the cell's whole life — which is what makes it usable while
// other goroutines hold the variable. A package-qualified name is a dependency
// type (`sync.Mutex`, `atomic.Int64`, `os.File`), and identity for those stays
// bashpp_task.go's to decide. Anything this function does not model reports
// "undecided" rather than guessing.
func (r *Runner) bashPPGoSourceSharableType(typ syntax.BashPPTypeExpr, depth int) (plain, decided bool) {
	if typ == nil {
		return false, false
	}
	if depth > 32 {
		// A type expression this deep is not one this function models. Report
		// undecided rather than granting: an unmodelled shape must never fall
		// through to "plain" merely by running out of budget.
		return false, false
	}
	switch typ := typ.(type) {
	case *syntax.BashPPNamedType:
		if typ.Name == nil {
			return false, false
		}
		if strings.Contains(typ.Name.Value, ".") {
			// Qualified by a package: a dependency type.
			return false, true
		}
		if _, imported := r.bashPPImports[typ.Name.Value]; imported {
			return false, true
		}
		if !bashPPGoSourcePredeclaredPlain[typ.Name.Value] {
			// An unqualified name this function cannot resolve. A locally
			// declared named struct or alias may EMBED a dependency handle
			// (`type counter struct { mu sync.Mutex }`), and an interface
			// spelling — `any`, `error`, a local interface — says nothing at
			// all about what the value dynamically holds. Answering "plain"
			// from the spelling would hand cross-task identity to a value this
			// spelling cannot describe.
			//
			// Report undecided rather than guessing. The caller then falls
			// through to bashPPGoSourcePlainPayload, which is total and fails
			// closed: a local struct — handle fields included — is shared as
			// one original Go variable, while a payload that IS a native
			// handle keeps bashpp_task.go's descriptor-copy rule.
			return false, false
		}
		for _, arg := range typ.TypeArgs {
			if arg == nil {
				return false, false
			}
			argPlain, argDecided := r.bashPPGoSourceSharableType(arg.ArgType, depth+1)
			if !argDecided {
				return false, false
			}
			if !argPlain {
				return false, true
			}
		}
		return true, true
	case *syntax.BashPPPointerType:
		// A pointer to a dependency value names that dependency object.
		return r.bashPPGoSourceSharableType(typ.Element, depth+1)
	case *syntax.BashPPCollectionType:
		// [N]T, []T and map[K]V are plain exactly when everything they can
		// hold is plain.
		if typ.Key != nil {
			keyPlain, keyDecided := r.bashPPGoSourceSharableType(typ.Key, depth+1)
			if !keyDecided {
				return false, false
			}
			if !keyPlain {
				return false, true
			}
		}
		return r.bashPPGoSourceSharableType(typ.Element, depth+1)
	}
	return false, false
}

// bashPPGoSourcePredeclaredPlain is the set of Go predeclared type names whose
// values are plain by construction: they cannot name or embed a dependency
// handle, so identity may be granted from the spelling alone. Interface
// spellings are deliberately absent — `any` and `error` describe no payload.
var bashPPGoSourcePredeclaredPlain = map[string]bool{
	"bool": true, "string": true, "byte": true, "rune": true,
	"int": true, "int8": true, "int16": true, "int32": true, "int64": true,
	"uint": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true,
	"uintptr": true, "float32": true, "float64": true,
	"complex64": true, "complex128": true,
}

// bashPPCellHoldsNative reports whether a cell's payload is, or contains, an
// imported native handle.
//
// It is total rather than depth-limited. An earlier revision stopped at depth 8
// and answered "no native here", which GRANTS identity to whatever sits below —
// a native handle nested ten containers deep would have been shared as if it
// were a plain value. Recursion is bounded by a visited set over the container
// pointers instead, so a cyclic payload terminates without a depth cap, and any
// payload shape this function does not model is reported as native-holding so
// that an unmodelled value is copied rather than aliased.
func bashPPCellHoldsNative(cell *bashPPCell) bool {
	if cell == nil {
		return false
	}
	seen := make(map[any]bool)
	var holds func(any) bool
	holds = func(value any) bool {
		switch value := value.(type) {
		case nil:
			return false
		case *bashPPBridgeValue:
			return true
		case bashPPBridgeValue:
			return true
		case map[string]any:
			key := reflect.ValueOf(value).Pointer()
			if seen[key] {
				return false
			}
			seen[key] = true
			for _, item := range bashPPStorageSnapshot(value) {
				if holds(item) {
					return true
				}
			}
			return false
		case []any:
			if len(value) == 0 {
				return false
			}
			key := reflect.ValueOf(value).Pointer()
			if seen[key] {
				return false
			}
			seen[key] = true
			for _, item := range value {
				if holds(item) {
					return true
				}
			}
			return false
		case string, bool, int, int8, int16, int32, int64,
			uint, uint8, uint16, uint32, uint64, float32, float64:
			return false
		}
		// Not a shape this function models. Fail closed: report it as native so
		// the cell is copied under the reviewed bashpp_task.go rule instead of
		// being aliased on an assumption.
		return true
	}
	return holds(cell.vr.Obj)
}
