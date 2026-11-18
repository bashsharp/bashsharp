package interp

import (
	"context"
	"fmt"
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

type bashPPCallbackFunction struct {
	templateFn  *bashPPFunc
	template    *Runner
	capture     map[*bashPPCell]bool
	templateErr string
}

// Function handles name interpreted closures in one dependency session. Only
// the typed trampoline is native; the body and captured cells stay here.
func (r *Runner) bashPPBridgeFunction(fn *bashPPFunc) (bashPPBridgeValue, error) {
	if fn.native != nil {
		return *fn.native, nil
	}
	iteratorYield, _ := r.goSourceIteratorYield(fn)
	makeFunc := r.bashPPReflectMakeFuncShape(fn)
	copiedResults := false
	copiedHandleResults := false
	callRefusal := ""
	reflecting := r.goSourceReflectingFunction
	r.goSourceReflectingFunction = false
	localCallback := r.goSourceLocalCallbackArg
	r.goSourceLocalCallbackArg = false
	localRefusal := ""
	for group, fields := range [][]*syntax.BashPPField{fn.params(), fn.results()} {
		if makeFunc {
			break
		}
		for _, field := range fields {
			if field.Variadic() {
				return bashPPBridgeValue{}, fmt.Errorf("gosource: variadic original callbacks are unsupported")
			}
			// Only reviewed synchronous consumers accept these aggregate results.
			// Callback arguments remain scalar: no copied reference aliasing.
			if group == 1 && r.bashPPTourCallbackResult(field.FieldTypeExpr) {
				continue
			}
			if group == 0 && iteratorYield != nil {
				if nested, ok := field.FieldTypeExpr.(*syntax.BashPPFuncType); ok &&
					bashPPFieldsSignature(nested.Params) == bashPPFieldsSignature(iteratorYield.Params) &&
					bashPPFieldsSignature(nested.Results) == bashPPFieldsSignature(iteratorYield.Results) {
					continue
				}
			}
			// A value-semantics aggregate — a struct or array built only from
			// scalars — is copied by Go itself at the call boundary, so the
			// copied transport observes what native Go observes. A parameter
			// naming an imported type is not copied at all: it stays the
			// dependency's own value behind a session handle.
			if r.bashPPCallbackValueType(field.FieldTypeExpr) || r.bashPPCallbackNativeType(field.FieldTypeExpr) {
				continue
			}
			if group == 0 && r.bashPPNativeType(field.FieldTypeExpr) {
				continue
			}
			// A slice of dependency values: as a parameter it arrives as the
			// dependency's own slice behind one handle, so element writes stay
			// shared; as a result it is rebuilt from its element handles, which
			// only a consumer that copies results out and never retains the
			// slice may observe (checked at the request, where it is known).
			if r.bashPPNativeHandleSlice(field.FieldTypeExpr) {
				copiedResults = copiedResults || group == 1
				copiedHandleResults = copiedHandleResults || group == 1
				continue
			}
			// A result-only counterpart of the rule above: a slice built from
			// pure value-semantics elements (scalars, or structs/arrays of
			// them) is rebuilt from copied element values on reply, exactly as
			// a handle slice is rebuilt from copied element handles. A
			// parameter keeps the copied-slice refusal below: unlike a handle
			// slice, whose elements alias the dependency's own storage, a
			// value slice's backing array is the interpreter's, and a copy of
			// it can silently drop writes an async or retained callback makes.
			if group == 1 && r.bashPPCallbackValueSlice(field.FieldTypeExpr) {
				copiedResults = true
				continue
			}
			// Interface parameters and results can carry nil, native handles, or
			// interpreter-owned dynamic values through the existing interface
			// side channel without flattening them to strings.
			if _, ok := r.bashPPInterfaceType(field.FieldTypeExpr); ok {
				continue
			}
			const refusal = "gosource: original callback signature requires value-semantics parameters and supported results"
			// reflect.ValueOf only wraps the function: its Pointer, Type and
			// Kind never transport a value, and Call is the one synchronous
			// use (see reflectedOriginalFunctionUse). A parameter the
			// dependency hands over as its own value behind a handle is bound
			// as that handle (bashPPRunCallbackFunc), so the callee shares
			// exactly the storage reflect passes it; any other signature
			// defers the refusal to Call.
			if reflecting {
				if group == 1 || !r.goSourceReflectHandleParam(field.FieldTypeExpr, 0) {
					callRefusal = refusal
				}
				continue
			}
			// The generic slices callback helpers are answered in this process
			// over the original backing array. A pointer parameter receives the
			// pointer from a live element cell, so its pointee identity remains
			// shared without transporting it to the dependency. Interfaces were
			// admitted above and use the same live-cell path. Keep the refusal in
			// case this pointer callback is ever routed outside that path.
			if localCallback && group == 0 {
				if r.callbackLocalSliceType(field.FieldTypeExpr, 0) {
					continue
				}
				if _, pointer := field.FieldTypeExpr.(*syntax.BashPPPointerType); pointer {
					localRefusal = refusal
					continue
				}
			}
			return bashPPBridgeValue{}, fmt.Errorf(refusal)
		}
	}
	req, err := r.bashPPEvalRequest()
	if err != nil {
		return bashPPBridgeValue{}, err
	}
	registered, err := r.bashPPCallbackFunctionTemplate(fn)
	if err != nil {
		return bashPPBridgeValue{}, err
	}
	s := req.Bridge
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.functions == nil {
		s.functions = map[uint64]*bashPPFunc{}
	}
	if s.functionOwners == nil {
		s.functionOwners = map[uint64]*bashPPCallbackFunction{}
	}
	for id, existing := range s.functions {
		if existing == fn {
			if registered.template != nil {
				registered.template.closeDirFile()
			}
			return bashPPBridgeValue{Kind: "callback", Handle: id, Session: s.id, Callbacks: true, copiedResults: copiedResults, copiedHandleResults: copiedHandleResults, callRefusal: callRefusal, localRefusal: localRefusal}, nil
		}
	}
	s.functionNext++
	id := s.functionNext
	s.functions[id] = fn
	s.functionOwners[id] = registered
	return bashPPBridgeValue{Kind: "callback", Handle: id, Session: s.id, Callbacks: true, copiedResults: copiedResults, copiedHandleResults: copiedHandleResults, callRefusal: callRefusal, localRefusal: localRefusal, newCallback: true}, nil
}

func (r *Runner) bashPPCallbackFunctionTemplate(fn *bashPPFunc) (*bashPPCallbackFunction, error) {
	capture, exact := r.bashPPCallbackFunctionCapture(fn)
	if !exact {
		// Registration does not prove how the dependency will use this value.
		// Keep the existing admission path authoritative: an unknown retained
		// consumer still earns its established refusal, while a testing
		// scheduler which needs an independent frame refuses below.
		return &bashPPCallbackFunction{
			templateErr: "original callback capture contains an unsupported construct",
		}, nil
	}
	// Package variables are shared Go storage even though they are not lexical
	// captures. Resolve their exact cells while registration still owns the
	// source frame; later native testing callbacks must not inspect the live
	// Runner or reconstruct package ownership from names alone.
	capture = r.bashPPCallbackSharedCells(fn, capture)
	// Capture the execution environment while the registering runner is its
	// sole owner. A later native scheduler callback must never clone or walk
	// that runner: it may be paused in another callback frame by then.
	saved := r.bashPPGoSourceCapture
	r.bashPPGoSourceCapture = capture
	template := r.subshellWithBashPPCapture(true, capture)
	r.bashPPGoSourceCapture = saved
	// The template — and through it every frame of this callback — now holds
	// the registering runner's own program tables. This runner keeps executing
	// (it registers the remaining subtests and runs on), so it too becomes a
	// copy-on-write holder rather than continuing to mutate tables a frame may
	// be reading; see [bashPPSharedTables]. This is the publishing runner's own
	// goroutine, which is what makes the flags safe to set without a lock.
	r.bashPPShareTables()
	// This is immutable template configuration, not live invocation state.
	// Every later frame clone reads it without touching the registering runner.
	template.bashPPGoSourceCapture = capture
	template.bashPPTools.callbackDepth = 0
	template.bashPPTools.routedDepth = 0
	template.bashPPTools.testingCallbackFrames = nil
	template.exit = exitStatus{}
	template.lastExit = exitStatus{}

	cloner := newBashPPClonerFor(template)
	cloner.shared = capture
	cloner.goSourceTask = true
	cloner.exactCapture = true
	return &bashPPCallbackFunction{
		templateFn: fn.cloned(cloner),
		template:   template,
		capture:    capture,
	}, nil
}

func (r *Runner) bashPPCallbackFunctionCapture(fn *bashPPFunc) (map[*bashPPCell]bool, bool) {
	body, bound := bashPPCallbackFunctionBody(fn)
	if fn == nil || fn.scope == nil || body == nil {
		return map[*bashPPCell]bool{}, true
	}
	free, exact := bashPPGoSourceFreeNames(body, bound)
	if !exact {
		return nil, false
	}
	capture := make(map[*bashPPCell]bool)
	for name := range free {
		if cell := fn.scope.lookup(name); cell != nil {
			if cell.viewConstant() || r.bashPPGoSourceSharable(cell) {
				capture[cell] = true
			}
		}
	}
	if fn.receiver != nil && r.bashPPGoSourceSharable(fn.receiver) {
		capture[fn.receiver] = true
	}
	return capture, true
}

func bashPPCallbackFunctionBody(fn *bashPPFunc) (*syntax.Block, map[string]bool) {
	bound := make(map[string]bool)
	if fn == nil {
		return nil, bound
	}
	var params, results []*syntax.BashPPField
	switch {
	case fn.decl != nil || fn.lit != nil:
		params, results = fn.params(), fn.bodyResults()
	}
	for _, param := range bashppParams(params) {
		if param.name != "" {
			bound[param.name] = true
		}
	}
	for _, result := range bashppParams(results) {
		if result.name != "" {
			bound[result.name] = true
		}
	}
	if fn.rangeYield != nil && fn.rangeYield.rng != nil {
		for _, name := range fn.rangeYield.rng.Names {
			if name != nil && name.Value != "_" {
				bound[name.Value] = true
			}
		}
		return fn.rangeYield.rng.Body, bound
	}
	if fn.decl != nil {
		return fn.decl.Body, bound
	}
	if fn.lit != nil {
		return fn.lit.Body, bound
	}
	return nil, bound
}

func (s *bashPPNativeSession) callbackFunction(id uint64) *bashPPCallbackFunction {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if cb := s.functionOwners[id]; cb != nil {
		return cb
	}
	return nil
}

func (s *bashPPNativeSession) originalCallbackFunction(id uint64) *bashPPFunc {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.functions[id]
}

func (s *bashPPNativeSession) closeCallbackFunctionTemplates() {
	if s == nil {
		return
	}
	s.mu.Lock()
	registered := s.functionOwners
	s.functionOwners = nil
	s.mu.Unlock()
	for _, callback := range registered {
		if callback != nil && callback.template != nil {
			callback.template.closeDirFile()
		}
	}
}

func bashPPNewCallbackHandles(q bashPPBridgeRequest) []uint64 {
	seen := make(map[uint64]bool)
	var visit func(bashPPBridgeValue)
	visit = func(value bashPPBridgeValue) {
		if value.Kind == "callback" && value.newCallback && value.Handle != 0 {
			seen[value.Handle] = true
		}
		for _, child := range value.Elements {
			visit(child)
		}
		for _, child := range value.Fields {
			visit(child)
		}
		for _, entry := range value.Entries {
			visit(entry.Key)
			visit(entry.Value)
		}
	}
	if q.Receiver != nil {
		visit(*q.Receiver)
	}
	for _, arg := range q.Args {
		visit(arg)
	}
	handles := make([]uint64, 0, len(seen))
	for handle := range seen {
		handles = append(handles, handle)
	}
	return handles
}

func (s *bashPPNativeSession) releaseCallbackFunctions(handles []uint64) {
	if s == nil || len(handles) == 0 {
		return
	}
	callbacks := make([]*bashPPCallbackFunction, 0, len(handles))
	s.mu.Lock()
	for _, handle := range handles {
		delete(s.functions, handle)
		if callback := s.functionOwners[handle]; callback != nil {
			callbacks = append(callbacks, callback)
			delete(s.functionOwners, handle)
		}
	}
	s.mu.Unlock()
	for _, callback := range callbacks {
		if callback.template != nil {
			callback.template.closeDirFile()
		}
	}
}

// goSourceReflectValueOfOperand reports the operand of a reflect.ValueOf
// call that denotes a function value directly.
func goSourceReflectValueOfOperand(imports map[string]string, q bashPPBridgeRequest, expr syntax.BashPPExpr) bool {
	alias, name, ok := strings.Cut(q.Selector, ".")
	if q.Op != "call" || q.Receiver != nil || !ok || imports[alias] != "reflect" || name != "ValueOf" {
		return false
	}
	for {
		paren, ok := expr.(*syntax.BashPPParenExpr)
		if !ok {
			break
		}
		expr = paren.X
	}
	switch expr.(type) {
	case *syntax.BashPPIdent, *syntax.BashPPFuncLit:
		return true
	}
	return false
}

// goSourceReflectHandleParam reports a parameter type whose value a
// reflected Call hands the callback as the dependency's own value behind a
// handle, with nothing the interpreter keeps elsewhere: scalars, imported
// types, and slices, arrays, maps, structs and pointers built from them. A
// pointer to a program type is excluded: it would name the dependency's
// mirror of that type, not the program's own storage.
func (r *Runner) goSourceReflectHandleParam(typ syntax.BashPPTypeExpr, depth int) bool {
	if typ == nil || depth > 8 {
		return false
	}
	if r.bashPPCallbackScalarType(typ) || r.bashPPCallbackNativeType(typ) {
		return true
	}
	switch shape := r.bashPPUnderlyingType(typ).(type) {
	case *syntax.BashPPStructType:
		for _, field := range bashPPFlatFields(shape.Fields) {
			if !r.goSourceReflectHandleParam(field.typ, depth+1) {
				return false
			}
		}
		return true
	case *syntax.BashPPCollectionType:
		switch shape.Kind {
		case "slice", "array":
			return r.goSourceReflectHandleParam(shape.Element, depth+1)
		}
	case *syntax.BashPPPointerType:
		if r.bashPPCallbackNativeType(shape.Element) {
			return true
		}
		if named, ok := shape.Element.(*syntax.BashPPNamedType); ok {
			text := bashPPTypeText(named)
			return bashPPBuiltinType(text) && text != "error" && text != "struct"
		}
		return r.goSourceReflectHandleParam(shape.Element, depth+1)
	}
	return false
}

// bashPPFunctionTypeText renders an original function's type in the original
// program's own spellings — the names the worker's type resolver is registered
// under. It reports false for a signature it cannot spell completely.
func bashPPFunctionTypeText(fn *bashPPFunc) (string, bool) {
	render := func(fields []*syntax.BashPPField) ([]string, bool) {
		var out []string
		for _, p := range bashppParams(fields) {
			if p.typ == nil {
				return nil, false
			}
			text := bashPPTypeText(p.typ)
			if text == "" {
				return nil, false
			}
			if p.variadic {
				text = "..." + text
			}
			out = append(out, text)
		}
		return out, true
	}
	params, ok := render(fn.params())
	if !ok {
		return "", false
	}
	results, ok := render(fn.results())
	if !ok {
		return "", false
	}
	text := "func(" + strings.Join(params, ", ") + ")"
	switch len(results) {
	case 0:
	case 1:
		text += " " + results[0]
	default:
		text += " (" + strings.Join(results, ", ") + ")"
	}
	return text, true
}

func (r *Runner) bashPPNativeFunctionCallback(ctx context.Context, id uint64, args []bashPPBridgeValue, frameFn *bashPPFunc) (values []bashPPBridgeValue, err error) {
	defer func() {
		if failure := recover(); failure != nil {
			values = nil
			err = fmt.Errorf("gosource: original function callback interpreter failure: %v", failure)
		}
	}()
	s := r.bashPPTools.bridge
	if frameFn == nil {
		frameFn = s.originalCallbackFunction(id)
		if frameFn == nil {
			return nil, fmt.Errorf("gosource: original callback handle expired")
		}
	}
	return r.bashPPRunCallbackFunc(ctx, frameFn, args)
}

// bashPPRunCallbackFunc executes one original function body on behalf of the
// dependency: a retained closure reached through its handle, or a package
// function an object companion called through a generated trampoline.
func (r *Runner) bashPPRunCallbackFunc(ctx context.Context, fn *bashPPFunc, args []bashPPBridgeValue) (values []bashPPBridgeValue, err error) {
	params := bashppParams(fn.params())
	if len(args) != len(params) {
		return nil, fmt.Errorf("gosource: original callback argument count mismatch")
	}
	savedResults, savedCalls := r.bashPPResultCells, r.bashPPCallCells
	savedChannels, savedInterfaces := r.bashPPCallChannels, r.bashPPCallInterfaces
	savedExit, savedPanic, savedCtx := r.exit, r.bashPPPanic, r.ectx
	failure := r.bashPPShortFailureSeq
	defer func() {
		r.bashPPResultCells, r.bashPPCallCells = savedResults, savedCalls
		r.bashPPCallChannels, r.bashPPCallInterfaces = savedChannels, savedInterfaces
		r.ectx = savedCtx
	}()
	r.ectx = ctx
	r.bashPPCallChannels = nil
	r.bashPPCallInterfaces = nil
	texts := make([]string, len(args))
	cells := make([]*bashPPCell, len(args))
	for i, arg := range args {
		if signature, ok := params[i].typ.(*syntax.BashPPFuncType); ok {
			if arg.Kind != "handle" || (!strings.HasPrefix(arg.Type, "func(") && !strings.HasPrefix(arg.NativeType, "func(")) {
				return nil, fmt.Errorf("gosource: callback parameter %d requires a native function", i)
			}
			native := arg
			callback := &bashPPFunc{native: &native, lit: &syntax.BashPPFuncLit{Params: signature.Params, Results: signature.Results}}
			vr := r.bashPPStoreFunc(callback)
			texts[i] = vr.Str
			cells[i] = &bashPPCell{vr: vr, declType: signature}
			continue
		}
		cell, text, err := r.goSourceCallbackCell(arg, params[i])
		if err != nil {
			if arg.Kind != "handle" && arg.Kind != "nil" {
				return nil, fmt.Errorf("gosource: callback parameter %d: %w", i, err)
			}
			cells[i] = goSourceNativeValueCell(arg)
			cells[i].declType = params[i].typ
			continue
		}
		cells[i], texts[i] = cell, text
	}
	r.bashPPResultCells = nil
	r.bashPPCallCells = cells
	entry := len(r.callStack)
	r.bashPPInvoke(ctx, fn, texts)
	if r.bashPPGoexiting() {
		r.bashPPPanic, r.exit = savedPanic, savedExit
		// The body ended by runtime.Goexit: the goroutine which raised
		// this callback ends too. See bashpp_goexit.go.
		return []bashPPBridgeValue{{Kind: bashPPGoexitKind}}, nil
	}
	if r.bashPPCallbackRaised(entry) && !r.exit.exiting {
		payload, goexit := r.bashPPPanic.value(), r.bashPPGoexiting()
		r.bashPPPanic, r.exit = savedPanic, savedExit
		if goexit {
			// The body ended by runtime.Goexit: the goroutine which raised
			// this callback ends too. See bashpp_goexit.go.
			return []bashPPBridgeValue{{Kind: bashPPGoexitKind}}, nil
		}
		return []bashPPBridgeValue{bashPPBridgePanicString(payload)}, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.exit.err != nil {
		return nil, r.exit.err
	}
	if r.exit.exiting || r.exit.fatalExit || r.exit.code != 0 || r.bashPPShortFailureSeq != failure {
		return nil, fmt.Errorf("gosource: original function callback failed (status %d)", r.exit.code)
	}
	want := bashppResultCount(fn.results())
	if want == 0 {
		return nil, nil
	}
	if len(r.bashPPResultCells) != want {
		return nil, fmt.Errorf("gosource: original callback result count mismatch")
	}
	for _, cell := range r.bashPPResultCells {
		v, err := r.bashPPBridgeCell(cell)
		if err != nil {
			return nil, err
		}
		values = append(values, v)
	}
	return values, nil
}

func synchronousFunctionCallback(req bashPPEvalRequest, q bashPPBridgeRequest) bool {
	if q.sourceSynchronousCallback {
		return true
	}
	path := ""
	if q.Receiver != nil && q.Receiver.Kind == "handle" {
		if q.Selector == "" && q.Receiver.Function && q.Receiver.Origin != 0 && q.Receiver.Callbacks {
			return true
		}
		// A bare call of a native function handle whose signature names a
		// program type is a func/method value the interpreter itself produced:
		// reflect over a local type M yields `Method(0).Func` of type
		// `func(main.M)`, and calling its Interface() trampolines synchronously
		// back into the interpreter's own method body. The call completes before
		// this request returns, exactly like the reviewed callbacks below; the
		// unsafe guard at the call site still refuses one that also hands the
		// dependency an interpreter-owned reference to mutate.
		if q.Selector == "" && q.Receiver.Function &&
			(strings.Contains(q.Receiver.NativeType, "main.") || strings.Contains(q.Receiver.Type, "main.")) {
			return true
		}
		if q.Receiver.Callable == "range-iterator" {
			return true
		}
		path = strings.TrimPrefix(q.Receiver.Callable, "*")
		if path == "sync.Once.Do" {
			return true
		}
		if q.Selector == "Do" && (q.Receiver.NativeType == "sync.Once" || q.Receiver.NativeType == "*sync.Once" || (q.Receiver.NativeType == "" && q.Receiver.Type == "sync.Once")) {
			return true
		}
		// T.Run does not return until a non-parallel subtest callback completes.
		// This is the path used by the reviewed Go-by-Example table test.
		if q.Selector == "Run" && (q.Receiver.NativeType == "testing.T" || q.Receiver.NativeType == "*testing.T") {
			return true
		}
		// M.Run is the scheduler testing.Main already stands on: Main is
		// MainStart(...).Run(), and Run returns its exit code only after every
		// test, benchmark, fuzz target and example it scheduled has completed.
		// The M handle itself is only ever minted by this session from a
		// MainStart that carried the callbacks it retains.
		if q.Selector == "Run" && (q.Receiver.NativeType == "testing.M" || q.Receiver.NativeType == "*testing.M") {
			return true
		}
		// Link.AllPos walks the inlining stack and invokes its visitor inline;
		// the callback is neither retained nor launched from another goroutine.
		if q.Selector == "AllPos" && (q.Receiver.NativeType == "cmd/internal/obj.Link" || q.Receiver.NativeType == "*cmd/internal/obj.Link") {
			return true
		}
	}
	if path == "" {
		alias, name, ok := strings.Cut(q.Selector, ".")
		if !ok {
			return false
		}
		path = req.Imports[alias] + "." + name
	}
	// ir.DoChildren immediately delegates to the node's generated doChildren
	// method. The generator emits only inline do(child) calls and synchronous
	// list loops, stopping as soon as one call returns true; no implementation
	// stores the callback or starts a goroutine.
	if path == "cmd/compile/internal/ir.DoChildren" || path == "golang.org/x/tour/wc.Test" || path == "golang.org/x/tour/pic.Show" || path == "path/filepath.WalkDir" || path == "testing.Main" {
		return true
	}
	// testing.AllocsPerRun stays refused: it would invoke the original func
	// synchronously, but its observable is the child's allocation count, and
	// the callback trampoline's own allocations are part of that measurement.
	// Serving it would answer with a number native Go never produces.
	if pkg, name, ok := strings.Cut(path, "."); ok {
		if pkg == "slices" {
			switch name {
			case "AppendSeq", "Collect", "Sorted", "SortedFunc", "SortedStableFunc":
				return true
			}
		}
		if pkg == "maps" && (name == "Collect" || name == "Insert") {
			return true
		}
	}
	pkg, name, ok := strings.Cut(path, ".")
	if !ok {
		return false
	}
	if pkg == "strings" || pkg == "bytes" {
		switch name {
		case "Map", "FieldsFunc", "ContainsFunc", "IndexFunc", "LastIndexFunc", "TrimFunc", "TrimLeftFunc", "TrimRightFunc":
			return true
		}
	}
	return false
}

// retainedFunctionCallback reports a registration API that keeps an original
// function past the call that hands it over and invokes it later, possibly from
// the dependency's own goroutines. Admitting one switches the whole session to
// callback-capable dispatch: every later request serves callbacks on its own
// parked goroutine, so a retained handler runs on the Runner that owns it and
// never concurrently with another interpreted frame.
func retainedFunctionCallback(req bashPPEvalRequest, q bashPPBridgeRequest) bool {
	path := ""
	if q.Receiver != nil && q.Receiver.Kind == "handle" {
		path = strings.TrimPrefix(q.Receiver.Callable, "*")
		if path == "" && q.Receiver.NativeType != "" {
			path = strings.TrimPrefix(q.Receiver.NativeType, "*") + "." + q.Selector
		}
	}
	if path == "" {
		alias, name, ok := strings.Cut(q.Selector, ".")
		if !ok {
			return false
		}
		path = req.Imports[alias] + "." + name
	}
	switch path {
	case "net/http.HandleFunc", "net/http.Handle",
		"net/http.ServeMux.HandleFunc", "net/http.ServeMux.Handle":
		return true
	case "cmd/compile/internal/types2.Scope.InsertLazy":
		// The scope keeps the resolver and runs it on the first lookup of
		// the name — a later request of this session (the unified export
		// data importers register every package-level object this way).
		return true
	case "testing.T.Cleanup", "testing.B.Cleanup", "testing.F.Cleanup":
		// The test keeps the function and runs it when the test and its
		// subtests finish, inside tRunner — while the request that runs the
		// tests (testing.Main, T.Run) is still parked serving callbacks.
		return true
	case "reflect.MakeFunc":
		// The made function retains its implementation and raises it on
		// every call of the result — a bare handle call that parks here.
		// Each bare call is routed to the runner that makes it
		// (routedCallbackRequest).
		return bashPPMadeFuncOwner(req)
	}
	return false
}

// These result shapes are consumed without mutation or retention by the reviewed
// Tour helpers. Arbitrary aggregate parameters/results require shared-reference
// transport and remain unsupported.
func (r *Runner) bashPPTourCallbackResult(typ syntax.BashPPTypeExpr) bool {
	shape, ok := r.bashPPUnderlyingType(typ).(*syntax.BashPPCollectionType)
	if !ok {
		return false
	}
	if shape.Kind == "map" {
		return bashPPTypeText(r.bashPPUnderlyingType(shape.Key)) == "string" && bashPPTypeText(r.bashPPUnderlyingType(shape.Element)) == "int"
	}
	if shape.Kind != "slice" {
		return false
	}
	inner, ok := r.bashPPUnderlyingType(shape.Element).(*syntax.BashPPCollectionType)
	if !ok || inner.Kind != "slice" {
		return false
	}
	element := bashPPTypeText(r.bashPPUnderlyingType(inner.Element))
	return element == "uint8" || element == "byte"
}

// bashPPNativeHandleSlice reports a slice whose elements name an imported
// dependency type ([]reflect.Value): every element is a session handle, so
// the slice carries no interpreter-owned storage of its own.
func (r *Runner) bashPPNativeHandleSlice(typ syntax.BashPPTypeExpr) bool {
	shape, ok := r.bashPPUnderlyingType(typ).(*syntax.BashPPCollectionType)
	if !ok || shape.Kind != "slice" {
		return false
	}
	return r.bashPPCallbackNativeType(shape.Element)
}

// bashPPCallbackValueSlice reports a slice whose elements have pure Go value
// semantics — scalars, or structs/arrays built only from such types (see
// bashPPCallbackValueType). Each element is copied by Go itself wherever it is
// read out of the slice, so the whole slice can be rebuilt from copied element
// values on reply, exactly as bashPPNativeHandleSlice rebuilds one from copied
// element handles.
func (r *Runner) bashPPCallbackValueSlice(typ syntax.BashPPTypeExpr) bool {
	shape, ok := r.bashPPUnderlyingType(typ).(*syntax.BashPPCollectionType)
	if !ok || shape.Kind != "slice" {
		return false
	}
	return r.bashPPCallbackValueType(shape.Element)
}

// resultOwnedFunctionCallback reports a dependency constructor that keeps an
// original function only inside the value it returns and never invokes it
// itself. reflect.MakeFunc wraps fn in the returned reflect.Value; that result
// (and every value derived from it) is marked callback-bearing, so the only
// way to reach fn again is a later request carrying the handle, which parks
// and serves the callback on the Runner that makes it. The session is not
// switched to retained dispatch, and reflect copies fn's results out of the
// returned slice without retaining it.
func resultOwnedFunctionCallback(req bashPPEvalRequest, q bashPPBridgeRequest) bool {
	if q.Receiver != nil {
		return false
	}
	alias, name, ok := strings.Cut(q.Selector, ".")
	if !ok {
		return false
	}
	switch req.Imports[alias] {
	case "reflect":
		return name == "MakeFunc"
	case "sync":
		return name == "OnceFunc" || name == "OnceValue" || name == "OnceValues"
	}
	return false
}

// callbackInertRequest reports a request that carries callback-bearing
// values but cannot invoke any of them while it is in flight: the
// reflect.MakeFunc registration itself, and reflect.Value.Interface on a
// value it made. Such a request needs no callback service, so it does not
// take the session's callback gate — a made function's callback that blocks
// (on a full channel, say) until this goroutine proceeds must not wait for it.
func callbackInertRequest(req bashPPEvalRequest, q bashPPBridgeRequest) bool {
	if resultOwnedFunctionCallback(req, q) {
		return true
	}
	return q.Op == "call" && q.Receiver != nil && q.Receiver.Kind == "handle" && q.Receiver.NativeType == "reflect.Value" &&
		q.Selector == "Interface" && len(q.Args) == 0
}

// copiedResultsConsumer reports a request whose callbacks' copied aggregate
// results are consumed without retention (see resultOwnedFunctionCallback).
func copiedResultsConsumer(req bashPPEvalRequest, q bashPPBridgeRequest) bool {
	return resultOwnedFunctionCallback(req, q)
}

// copiedHandleResultsConsumer narrows copiedResultsConsumer for a callback
// whose result is a slice of dependency handles ([]reflect.Value). Such a
// slice is rebuilt on the dependency side from its element handles, so only
// a consumer that copies the elements out and never keeps the slice may
// observe it: reflect.MakeFunc. A sync.Once* constructor caches the rebuilt
// slice forever, which is not the slice the original returned.
func copiedHandleResultsConsumer(req bashPPEvalRequest, q bashPPBridgeRequest) bool {
	if q.Receiver != nil {
		return false
	}
	alias, name, ok := strings.Cut(q.Selector, ".")
	return ok && req.Imports[alias] == "reflect" && name == "MakeFunc"
}
