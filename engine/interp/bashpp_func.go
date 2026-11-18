// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"context"
	"errors"
	"fmt"
	"go/constant"
	"go/types"
	"maps"
	"strconv"
	"strings"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/polyglot"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Evaluation of the Bash++ function nodes: the P3-A ("typed functions")
// declarations, `return` and `defer`, and the P3-B function LITERALS,
// closures and VARIADIC parameters.
//
// ONE INVOKER, TWO SPELLINGS. A literal is not a second kind of function: it
// is a [bashPPFunc] whose signature came from a [syntax.BashPPFuncLit] instead
// of a declaration, so parameter binding, results, defers and status bridging
// are the same code for both. What a literal adds is WHEN the lexical scope is
// captured — at each evaluation of the literal, not once per name — which is
// what makes two closures from the same factory hold different cells.
//
// TWO NAMESPACES, ALREADY RECONCILED. A Bash++ func binds its parameters and
// named results as TYPED lexical bindings, not as a second kind of shell
// variable, and it leans on a property vars.go already guarantees: an ordinary
// shell assignment to a name a lexical binding owns writes THROUGH to that
// binding (see [Runner.setVar]). So `func f() (n int) { n=5; return }` needs no
// special machinery — `n=5` updates the same cell the bare return reads, and a
// closure over an outer `var` observes writes for the same reason. Only the
// func's own locals live in the shell function scope pushed alongside.

// bashPPFunc is a callable Go-form function: its signature and body, plus the
// lexical scope captured where it was written, which its body's free
// identifiers close over rather than resolving them at the call site.
//
// A named declaration and a function literal are the same thing to every
// caller — same binding rules, same results, same defers — so they are one
// type with two spellings rather than two types with one duplicated invoker.
// Exactly one of decl and lit is set.
type bashPPFunc struct {
	goSourceReceiver *goSourceMethodBinding
	native           *bashPPBridgeValue
	rangeYield       *goSourceRangeYield
	collectYield     *[]bashPPBridgeValue
	runtimeError     *bashPPRuntimeErrorCall
	foreign          *bashPPForeignFunc
	decl             *syntax.BashPPFuncDecl
	lit              *syntax.BashPPFuncLit
	scope            *bashPPScope
	typeArgs         map[string]syntax.BashPPTypeExpr
	// bound is the name a literal was bound to by `:=`, kept only so that a
	// diagnostic can say which function the script means. It is not an
	// identity: the same closure may be copied to other names, and a later
	// binding of this one does not rename it.
	bound string
	// receiver is set on a resolved method call or method value. The method
	// declaration itself keeps it nil; resolution clones the function and
	// binds either a copied value cell or the addressable pointer cell.
	receiver *bashPPCell
	// skipArgs is one for a method expression T.M(v, ...), where v supplies
	// the receiver rather than the first ordinary parameter.
	skipArgs int
	// decoratorNext marks the predeclared Call.Next, answered natively by
	// [Runner.bashPPDecoratorNext] rather than by a body.
	decoratorNext bool
	// advised holds the policy decorators [Advice] added at registration;
	// they wrap the declaration's own, outermost first.
	advised []DecoratorSpec
	// goError marks @go.error(), which adapts the public result surface while
	// leaving the body/decorator chain on the original results.
	goError bool
	// scalarInt is an immutable, eligibility-checked execution plan for a
	// side-effect-free function whose complete signature and body use only
	// int values. Unsupported functions leave this nil and keep using the
	// general evaluator.
	scalarInt *bashPPScalarIntFunc
}

// bashPPType is one script-local named type. Aliases intentionally cannot own
// methods, matching Go's receiver declaration rule.
type bashPPType struct {
	underlying string
	alias      bool
	typeParams []*syntax.BashPPTypeParam
	members    []string
	typeExpr   syntax.BashPPTypeExpr
	fields     []*syntax.BashPPField
}

// name is what diagnostics call the function. A literal has none, so it is
// described by where it came from rather than given a fabricated identifier a
// reader would then look for in the script.
func (f *bashPPFunc) name() string {
	switch {
	case f.decl != nil:
		return f.decl.Name.Value
	case f.bound != "":
		return f.bound
	}
	return "func literal"
}

func (f *bashPPFunc) params() []*syntax.BashPPField {
	if f.decl != nil {
		if len(f.typeArgs) > 0 {
			return bashPPSubstituteFields(f.decl.Params, f.typeArgs)
		}
		return f.decl.Params
	}
	// A literal has no type parameters of its own, but one written inside a
	// generic body carries that instantiation's bindings, so its signature
	// substitutes them exactly as a declared function's does.
	if len(f.typeArgs) > 0 {
		return bashPPSubstituteFields(f.lit.Params, f.typeArgs)
	}
	return f.lit.Params
}

func (f *bashPPFunc) results() []*syntax.BashPPField {
	if f.decl != nil {
		results := f.decl.Results
		if len(f.typeArgs) > 0 {
			results = bashPPSubstituteFields(results, f.typeArgs)
		}
		if f.goError {
			results = append(append([]*syntax.BashPPField(nil), results...), bashPPGoErrorResultField())
		}
		return results
	}
	if len(f.typeArgs) > 0 {
		return bashPPSubstituteFields(f.lit.Results, f.typeArgs)
	}
	return f.lit.Results
}

func (f *bashPPFunc) bodyResults() []*syntax.BashPPField {
	if f.decl != nil {
		if len(f.typeArgs) > 0 {
			return bashPPSubstituteFields(f.decl.Results, f.typeArgs)
		}
		return f.decl.Results
	}
	return f.results()
}

func (f *bashPPFunc) typeParams() []*syntax.BashPPTypeParam {
	if f.decl != nil {
		return f.decl.TypeParams
	}
	return nil
}

// cloned copies f for a subshell, deep-copying its captured scope through the
// shared cloner so the aliasing between closures, and between a closure and the
// live scope, survives the copy.
func (f *bashPPFunc) cloned(c *bashPPCloner) *bashPPFunc {
	copied := *f
	copied.scope = c.clone(f.scope)
	copied.receiver = c.cloneCell(f.receiver)
	if f.typeArgs != nil {
		copied.typeArgs = make(map[string]syntax.BashPPTypeExpr, len(f.typeArgs))
		for name, typ := range f.typeArgs {
			copied.typeArgs[name] = typ
		}
	}
	return &copied
}

func (f *bashPPFunc) body() *syntax.Block {
	if f == nil {
		return nil
	}
	if f.rangeYield != nil && f.rangeYield.rng != nil {
		return f.rangeYield.rng.Body
	}
	if f.decl != nil {
		return f.decl.Body
	}
	if f.lit == nil {
		return nil
	}
	return f.lit.Body
}

// bashPPFuncHandlePrefix marks a string value as a reference into the runner's
// closure registry.
//
// WHY A HANDLE AND NOT A VALUE. A closure is a Go pointer with a captured
// scope; the shell's value model is strings, and every path a variable takes —
// expansion, `execve`, a subshell's private copy — assumes it can carry the
// value as bytes. A handle keeps that assumption true: the bytes are what the
// shell moves around, while the function itself stays on the runner, where the
// subshell cloner can copy it with the rest of the typed state. The prefix is
// deliberately unmistakable rather than opaque, so `echo $f` shows a reader
// what they are holding instead of a bare integer.
const bashPPFuncHandlePrefix = "func@bashpp:"

// bashPPMakeClosure evaluates a function literal to a callable value,
// capturing the lexical scope AT THIS MOMENT.
//
// The capture is per EVALUATION, not per literal: a literal written inside a
// loop body yields a different closure each time round, over that iteration's
// cells. That is Go's rule, and it is why the registry is appended to rather
// than memoized on the syntax node.
func (r *Runner) bashPPMakeClosure(lit *syntax.BashPPFuncLit) (*bashPPFunc, expand.Variable) {
	fn := r.bashPPNewClosure(lit)
	return fn, r.bashPPStoreFunc(fn)
}

// bashPPNewClosure is [Runner.bashPPMakeClosure] without the registry entry.
//
// The registry exists so a closure can travel as a handle string; it is
// append-only because a handle may have been copied anywhere. A literal that is
// the callee of its own call — `func() {…}()`, `defer func() {…}()`, `go
// func() {…}()` — never becomes a value, so no handle to it can exist and the
// caller (or the deferred entry) holds the only reference. Registering it
// anyway retained every such closure, and its captured scope, for the life of
// the runner: a loop over a function with `defer func() {}()` grew without
// bound.
func (r *Runner) bashPPNewClosure(lit *syntax.BashPPFuncLit) *bashPPFunc {
	fn := &bashPPFunc{lit: lit}
	if r.bashPPScope != nil {
		if r.bashPPGoSource && r.bashPPFuncActive == 0 {
			fn.scope = r.bashPPScope
		} else if r.bashPPGoSource {
			fn.scope = r.goSourceClosureScope(lit)
		} else {
			fn.scope = r.bashPPScope.snapshot()
		}
	}
	// A literal written inside a generic body is part of THAT instantiation:
	// its own body may name the enclosing `T`, and it keeps meaning the type
	// argument of the call that created the closure even if the closure
	// escapes and is invoked later. The bindings travel with the closure for
	// the same reason its captured scope does.
	fn.typeArgs = r.bashPPTypeParamArgs
	return fn
}

// bashPPFuncLitType is the concrete signature a function literal names as a
// value. A function-typed target — `var f func(int) int; f = func(n int) int
// {…}` — owns a func type, so the value it is given has to arrive naming one
// too. Without this the closure travels as a bare handle with no declared
// type, and reassigning it reads as an untyped scalar that is rejected as
// "not assignable to func(int)(int)". Building the type from the literal's own
// parameter and result fields keeps its identity exactly the signature the
// programmer wrote, which is what a recursive closure calling itself relies on.
func bashPPFuncLitType(lit *syntax.BashPPFuncLit) *syntax.BashPPFuncType {
	if lit == nil {
		return &syntax.BashPPFuncType{}
	}
	return &syntax.BashPPFuncType{
		Func:      lit.Pos(),
		Lparen:    lit.Lparen,
		Rparen:    lit.Rparen,
		Params:    lit.Params,
		Results:   lit.Results,
		ResLparen: lit.ResLparen,
		ResRparen: lit.ResRparen,
	}
}

func (r *Runner) bashPPStoreFunc(fn *bashPPFunc) expand.Variable {
	r.bashPPUnshareClosures()
	r.bashPPClosures = append(r.bashPPClosures, fn)
	return expand.Variable{
		Set:  true,
		Kind: expand.String,
		Str:  bashPPFuncHandlePrefix + strconv.Itoa(len(r.bashPPClosures)-1),
	}
}

// bashPPClosure resolves a handle back to the closure it names. An unknown or
// malformed handle is simply not a function, which is what lets an ordinary
// string variable share the namespace without ever being mistaken for one.
func (r *Runner) bashPPClosure(value string) (*bashPPFunc, bool) {
	rest, ok := strings.CutPrefix(value, bashPPFuncHandlePrefix)
	if !ok {
		return nil, false
	}
	i, err := strconv.Atoi(rest)
	if err != nil || i < 0 || i >= len(r.bashPPClosures) {
		return nil, false
	}
	return r.bashPPClosures[i], true
}

// bashPPDeferred is one entry on the deferred-call stack: the call to run when
// the enclosing func returns, and the arguments captured — already evaluated —
// at the point `defer` ran, which is what gives Go's "arguments are evaluated
// when the defer statement executes" rule.
type bashPPDeferred struct {
	builtin func()
	native  func(context.Context) error
	testing func()
	agentic bool
	call    *syntax.BashPPCall
	// fn is the function resolved AT DEFER TIME, which matters for a closure:
	// `defer f(1)` must run the f that was current when the defer executed,
	// not whatever f names when the frame unwinds. It is nil when the deferred
	// callee is an ordinary shell command.
	fn *bashPPFunc
	// predeclared names the Bash++ predeclared function deferred, when the
	// callee is one: `defer panic(v)`. It is resolved at defer time like fn,
	// so a later declaration shadowing the name cannot change what unwinds.
	predeclared string
	args        []string
	cells       []*bashPPCell
	// captured holds the cells a deferred value builtin's synthetic argument
	// names resolve to. They ride the entry because the block that ran the
	// defer statement (a loop body, an inner block) is gone by unwind time.
	captured map[string]*bashPPCell
}

// bashPPReturnState carries a Go-form return out through the body's statement
// loop to the invoker. active records that a return fired at all, which
// distinguishes a bare `return` (values nil) from falling off the end of the
// body.
type bashPPReturnState struct {
	active bool
	values []string
	cells  []*bashPPCell
}

// bashPPFuncDecl registers a typed function, capturing the lexical environment
// it was written in so the body closes over its definition site.
func (r *Runner) bashPPFuncDecl(d *syntax.BashPPFuncDecl) {
	if r.bashPPHoistedDecls[d] {
		delete(r.bashPPHoistedDecls, d)
		return
	}
	if !r.objectsEnabled() {
		r.errf("bash++ function declaration evaluated with extensions disabled\n")
		r.exit = exitStatus{code: 2}
		return
	}
	name := d.Name.Value
	if required := bashppRequiredAfterDefault(d.Params); required != "" {
		r.errf("BASHPP-EDEFAULT-ORDER: required parameter %q follows a default parameter\n", required)
		r.exit = exitStatus{code: 2}
		return
	}
	if !syntax.BashPPValidIdent(name) {
		r.errf("invalid function name: %q\n", name)
		r.exit = exitStatus{code: 2}
		return
	}
	if err := bashPPValidateTypeParamDecls(d.TypeParams); err != nil {
		r.errf("%v\n", err)
		r.exit.code = 2
		return
	}
	if !r.bashPPCheckEnumSwitches(d) {
		return
	}
	if d.Receiver != nil {
		r.bashPPMethodDecl(d)
		return
	}
	// The blank identifier declares no package-level function. Go permits any
	// number of `func _` declarations (stringer emits one in each generated
	// file), and none can be called or participate in a redeclaration.
	if name == "_" {
		return
	}
	if bashPPGoErrorDecorated(d) {
		if len(d.TypeParams) > 0 || bashPPTrailingErrorResult(d.Results) {
			r.errf("BASHPP-EDECO-GOERROR: @go.error requires a receiver-less non-generic function with no trailing error result\n")
			r.exit.code = 2
			return
		}
	}
	r.bashPPUnshareFuncs()
	if r.bashPPFuncs == nil {
		r.bashPPFuncs = make(map[string]*bashPPFunc, 4)
	}
	if _, exists := r.bashPPFuncs[name]; exists {
		r.errf("function %s redeclared in this session\n", name)
		r.exit.code = 2
		return
	}
	advised, ok := r.bashPPRegisterDecorators(name, d.Pos(), d.Decorators, d.Agentic != nil)
	if !ok {
		return
	}
	var captured *bashPPScope
	if r.bashPPScope != nil {
		if r.bashPPGoSource {
			captured = r.bashPPScope
		} else {
			captured = r.bashPPScope.snapshot()
		}
	}
	fn := &bashPPFunc{decl: d, scope: captured, advised: advised, goError: bashPPGoErrorDecorated(d)}
	fn.scalarInt = bashPPCompileScalarIntFunc(fn)
	r.bashPPFuncs[name] = fn
}

func bashPPValidateTypeParamDecls(params []*syntax.BashPPTypeParam) error {
	seen := make(map[string]bool)
	for _, group := range params {
		if group == nil || group.Constraint == nil {
			return fmt.Errorf("BASHPP-EGENERIC-PARAM: type parameter constraint is missing")
		}
		for _, name := range group.Names {
			if name == nil || !syntax.BashPPValidIdent(name.Value) {
				return fmt.Errorf("BASHPP-EGENERIC-PARAM: invalid type parameter")
			}
			// The blank identifier declares nothing and may be repeated.
			if seen[name.Value] && name.Value != "_" {
				return fmt.Errorf("BASHPP-EGENERIC-PARAM: type parameter %s redeclared", name.Value)
			}
			seen[name.Value] = true
		}
	}
	return nil
}

// bashPPCheckEnumSwitches validates exhaustiveness when the function is
// declared, rather than waiting for a call. That gives an uncalled function
// the same fail-closed behavior as a compiled enum switch.
func (r *Runner) bashPPCheckEnumSwitches(d *syntax.BashPPFuncDecl) bool {
	types := make(map[string]string)
	for _, field := range d.Params {
		if field.FieldType == nil {
			continue
		}
		for _, name := range field.Names {
			types[name.Value] = field.FieldType.Value
		}
	}
	var checkStmts func([]*syntax.Stmt) bool
	checkStmts = func(stmts []*syntax.Stmt) bool {
		for _, stmt := range stmts {
			sw, ok := stmt.Cmd.(*syntax.BashPPSwitch)
			if !ok {
				continue
			}
			ident, isIdent := sw.Tag.(*syntax.BashPPIdent)
			typeName := ""
			if isIdent {
				typeName = types[ident.Name.Value]
			}
			if typ := r.bashPPTypes[typeName]; len(typ.members) > 0 {
				covered := make(map[string]bool)
				hasDefault := false
				for _, arm := range sw.Arms {
					if len(arm.Exprs) == 0 {
						hasDefault = true
					} else {
						for _, expr := range arm.Exprs {
							if member, ok := expr.(*syntax.BashPPIdent); ok {
								covered[member.Name.Value] = true
							}
						}
					}
				}
				if !hasDefault {
					for _, member := range typ.members {
						if !covered[member] {
							r.errf("BASHPP-EENUM-NONEXHAUSTIVE: switch on %s is missing member %s or a default arm\n", typeName, member)
							r.exit = exitStatus{code: 2}
							return false
						}
					}
				}
			}
			for _, arm := range sw.Arms {
				if !checkStmts(arm.Stmts) {
					return false
				}
			}
		}
		return true
	}
	return d.Body == nil || checkStmts(d.Body.Stmts)
}

func (r *Runner) bashPPMethodDecl(d *syntax.BashPPFuncDecl) {
	if bashPPGoErrorDecorated(d) {
		r.errf("BASHPP-EDECO-GOERROR: @go.error requires a receiver-less function\n")
		r.exit.code = 2
		return
	}
	recv := d.Receiver
	for _, field := range append(append([]*syntax.BashPPField(nil), d.Params...), d.Results...) {
		for _, name := range field.Names {
			if recv.Name != nil && name.Value == recv.Name.Value && name.Value != "_" {
				r.errf("receiver %s redeclared in method signature\n", recv.Name.Value)
				r.exit.code = 2
				return
			}
		}
	}
	typ, ok := r.bashPPTypes[recv.RecvType.Value]
	if !ok {
		r.errf("invalid receiver type %s (type is not declared in this session)\n", recv.RecvType.Value)
		r.exit.code = 2
		return
	}
	if typ.alias {
		r.errf("invalid receiver type %s (cannot define methods on an alias)\n", recv.RecvType.Value)
		r.exit.code = 2
		return
	}
	wantParams := bashPPTypeParamCount(typ.typeParams)
	if len(recv.TypeParams) != wantParams {
		r.errf("BASHPP-EGENERIC-RECEIVER: %s expects %d receiver type parameter(s); got %d\n", recv.RecvType.Value, wantParams, len(recv.TypeParams))
		r.exit.code = 2
		return
	}
	seenParams := make(map[string]bool, len(recv.TypeParams))
	for _, param := range recv.TypeParams {
		if seenParams[param.Value] && param.Value != "_" {
			r.errf("BASHPP-EGENERIC-RECEIVER: receiver type parameter %s redeclared\n", param.Value)
			r.exit.code = 2
			return
		}
		seenParams[param.Value] = true
	}
	if r.bashPPGoSource && d.Name.Value == "_" {
		// Blank methods are checked declarations, not entries in a method set.
		return
	}
	r.bashPPUnshareMethods()
	if r.bashPPMethods == nil {
		r.bashPPMethods = make(map[string]map[string]*bashPPFunc)
	}
	methods := r.bashPPMethods[recv.RecvType.Value]
	if methods == nil {
		methods = make(map[string]*bashPPFunc)
		r.bashPPMethods[recv.RecvType.Value] = methods
	}
	if _, exists := methods[d.Name.Value]; exists {
		r.errf("method %s.%s redeclared in this session\n", recv.RecvType.Value, d.Name.Value)
		r.exit.code = 2
		return
	}
	advised, ok := r.bashPPRegisterDecorators(recv.RecvType.Value+"."+d.Name.Value, d.Pos(), d.Decorators, d.Agentic != nil)
	if !ok {
		return
	}
	var captured *bashPPScope
	if r.bashPPScope != nil {
		if r.bashPPGoSource {
			captured = r.bashPPScope
		} else {
			captured = r.bashPPScope.snapshot()
		}
	}
	methods[d.Name.Value] = &bashPPFunc{decl: d, scope: captured, advised: advised}
	r.bashPPInvalidateSelectionCache()
}

// bashPPLookupFunc resolves a call's callee to a callable function: a literal
// in callee position, a function declared with `func`, or a name bound to a
// closure. Selector calls first resolve a local typed root; only an absent
// local root may fall through to package-import evaluation.
//
// A literal is EVALUATED here, which is the correct moment: `func(n int) { …
// }(1)` captures the scope at the point of the call, exactly as the same
// literal bound to a name captures it at the point of the binding.
func (r *Runner) bashPPLookupFunc(c *syntax.BashPPCall) (*bashPPFunc, bool) {
	if pin := r.bashPPGoSourcePin; pin != nil && pin.call == c {
		// A launched task's callee was resolved once, in the parent. Re-running
		// a computed callee here would evaluate it twice; re-reading a variable
		// could find a different function than the `go` statement launched.
		if pin.bound != nil {
			return pin.bound, true
		}
		return r.bashPPClosure(pin.handle)
	}
	if recv, method, ok := r.goSourceMethodExprCallee(c); ok {
		fn, err := r.goSourceMethodExprClosure(recv, method)
		if err != nil {
			r.exit.fatal(err)
			return nil, false
		}
		return fn, true
	}
	if r.bashPPGoSource && c.CalleeExpr != nil {
		if method, ok := c.CalleeExpr.(*syntax.BashPPSelectorExpr); ok && method.MethodValue && !r.bashPPNativeExpr(method.X) {
			fn, err := r.goSourceLocalMethod(method, len(c.Args) > 0)
			if err != nil {
				if !r.bashPPPanicking() {
					r.exit.fatal(err)
				}
				return nil, false
			}
			return r.bashPPInstantiateFunc(c, fn)
		}
		cell, err := r.goSourceComputedCalleeCell(c.CalleeExpr)
		if err != nil {
			r.exit.fatal(err)
			return nil, false
		}
		closureName := cell.vr.Str
		if closureName == "" && cell.vr.Kind == expand.Object {
			closureName, _ = cell.vr.Obj.(string)
		}
		if closureName == "" {
			closureName = cell.vr.String()
		}
		if fn, ok := r.bashPPClosure(closureName); ok {
			return fn, true
		}
		return r.goSourceComputedNativeFuncCell(c, cell)
	}
	if c.FuncLit != nil {
		return r.bashPPNewClosure(c.FuncLit), true
	}
	foreignName := make([]string, len(c.Fun))
	for i, part := range c.Fun {
		foreignName[i] = part.Value
	}
	if fn := r.bashPPForeignFuncs[strings.Join(foreignName, ".")]; fn != nil {
		if fn.foreign.export.Unavailable != "" {
			r.exit.fatal(errors.New(fn.foreign.export.UnavailableDiagnostic()))
			return nil, false
		}
		return fn, true
	}
	if len(c.Fun) == 2 {
		if module := r.bashPPImportModule(c.Fun[0].Value); module != nil {
			name := c.Fun[1].Value
			return bashPPDirectForeign(module, nil, name, strings.Join(foreignName, ".")), true
		}
	}
	if len(c.Fun) >= 1 && r.bashPPScope != nil {
		if vr := r.bashPPScope.lookup(c.Fun[0].Value).viewVar(); vr.Kind == expand.Object {
			if handle, ok := vr.Obj.(*polyglot.Handle); ok {
				var middle []*syntax.Lit
				if len(c.Fun) > 2 {
					middle = c.Fun[1 : len(c.Fun)-1]
				}
				for _, part := range middle {
					result, err := handle.GetAttr(r.ectx, part.Value)
					if err != nil {
						r.exit.fatal(err)
						return nil, false
					}
					handle, ok = result.Value.(*polyglot.Handle)
					if !ok {
						r.exit.fatal(fmt.Errorf("Python attribute %s is not an object", part.Value))
						return nil, false
					}
				}
				name := ""
				if len(c.Fun) > 1 {
					name = c.Fun[len(c.Fun)-1].Value
				}
				return bashPPDirectForeign(nil, handle, name, strings.Join(foreignName, ".")), true
			}
		}
	}
	if len(c.Fun) >= 2 {
		// A selector call resolves to its method first and is instantiated
		// second, exactly as a plain call is. A method's own type parameters
		// are independent of the receiver's, so the receiver bindings the
		// binding step recorded are already on the function when the
		// instantiation step adds the method's; see [Runner.bashPPInstantiateFunc].
		fn, ok := r.bashPPLookupSelectorFunc(c)
		if !ok {
			// ForeignModules also includes imports whose exports resolve
			// lazily. Reject a fence name only after imports, object handles,
			// and selectors have had a chance to resolve the call.
			for _, module := range r.bashPPForeignModules {
				if module.Plan().Alias == c.Fun[0].Value {
					r.exit.fatal(fmt.Errorf("bash++: unknown function %s", strings.Join(foreignName, ".")))
					break
				}
			}
			return nil, false
		}
		return r.bashPPInstantiateFunc(c, fn)
	}
	if len(c.Fun) != 1 {
		return nil, false
	}
	name := c.Fun[0].Value
	// A block-scoped closure of the same name shadows the package function;
	// see bashpp_sprint165_runtime2_shadow.go.
	if fn, ok := r.bashPPScopedClosureCallee(name); ok {
		return r.bashPPInstantiateFunc(c, fn)
	}
	if fn, ok := r.bashPPFuncs[name]; ok {
		return r.bashPPInstantiateFunc(c, fn)
	}
	// A closure held in a variable is callable by that variable's name, which
	// is what makes `greet := func(…) { … }; greet(x)` and a returned factory
	// closure work without a second call syntax.
	if vr := r.lookupVar(name); vr.Kind == expand.String {
		fn, ok := r.bashPPClosure(vr.Str)
		if !ok {
			return nil, false
		}
		return r.bashPPInstantiateFunc(c, fn)
	}
	return nil, false
}

func bashPPDirectForeign(module *polyglot.Module, receiver *polyglot.Handle, name, qualified string) *bashPPFunc {
	decl := &syntax.BashPPFuncDecl{
		Name:    &syntax.Lit{Value: name},
		Params:  []*syntax.BashPPField{{Names: []*syntax.Lit{{Value: "args"}}, FieldType: &syntax.Lit{Value: "any"}, Ellipsis: syntax.NewPos(0, 1, 1)}},
		Results: []*syntax.BashPPField{{FieldType: &syntax.Lit{Value: "any"}}},
	}
	return &bashPPFunc{decl: decl, foreign: &bashPPForeignFunc{module: module, receiver: receiver, export: polyglot.Export{Name: name, Signature: polyglot.Signature{Results: []string{"any"}}}, qualified: qualified, direct: true}}
}

// bashPPLookupSelectorFunc resolves the `x.M`, `T.M` and `(*T).M` callee forms
// to the method they name, without instantiating it.
func (r *Runner) bashPPLookupSelectorFunc(c *syntax.BashPPCall) (*bashPPFunc, bool) {
	owner := c.Fun[0].Value
	// A local value is always considered before an import binding. This is
	// deterministic even when the import registry contains the same name.
	if cell := r.bashPPScope.lookup(owner); cell != nil {
		_, typeName := r.bashPPTypes[owner]
		if !typeName || cell.viewInterface() != nil || bashPPSelectorCellType(cell) != nil {
			return r.bashPPBindLocalSelector(c, cell)
		}
	}
	if len(c.Fun) != 2 {
		return nil, false
	}
	method := c.Fun[1].Value
	// T.M(v, ...) selects from T's method set; (*T).M(p, ...) records the
	// pointer method-expression spelling on the call node.
	if _, localType := r.bashPPTypes[owner]; localType {
		rootType := syntax.BashPPTypeExpr(&syntax.BashPPNamedType{Name: &syntax.Lit{Value: owner}})
		if c.PointerMethodExpr {
			rootType = &syntax.BashPPPointerType{Element: rootType}
		}
		sel := r.bashPPResolveSelection(rootType, method, true, false)
		if sel.ambiguous {
			r.errf("BASHPP-ESELECTOR-AMBIGUOUS: ambiguous selector %s.%s\n", bashPPTypeText(rootType), method)
			r.exit.code = 2
			return nil, false
		}
		fn := sel.method
		if fn == nil && sel.interfaceSpec == nil {
			r.errf("%s.%s is not in the method set of %s\n", owner, method, owner)
			r.exit.code = 2
			return nil, false
		}
		if len(c.Args) == 0 {
			r.errf("not enough arguments in call to method expression %s.%s\n", owner, method)
			r.exit.code = 2
			return nil, false
		}
		cell := r.bashPPCellForWord(c.Args[0])
		var actualType syntax.BashPPTypeExpr
		if cell != nil {
			actualType = cell.view().declType
			if actualType == nil {
				if meta := bashPPCellMeta(cell); meta != nil {
					actualType = meta.typ
				}
			}
		}
		if cell == nil || bashPPTypeText(actualType) != bashPPTypeText(rootType) {
			r.errf("cannot use first argument as %s receiver in %s.%s\n", owner, owner, method)
			r.exit.code = 2
			return nil, false
		}
		bound, ok := r.bashPPBindPromotedMethod(cell, method, sel, false)
		if !ok {
			return nil, false
		}
		bound.skipArgs = 1
		return bound, true
	}
	return nil, false
}

func bashPPSelectorCellType(cell *bashPPCell) syntax.BashPPTypeExpr {
	cell = cell.view()
	if cell == nil {
		return nil
	}
	typ := cell.declType
	if typ == nil {
		if meta := bashPPCellMeta(cell); meta != nil {
			typ = meta.typ
		}
	}
	if typ == nil && cell.typeName != "" {
		typ, _ = bashPPScalarNamedType(cell.typeName)
		if cell.pointer {
			typ = &syntax.BashPPPointerType{Element: typ}
		}
	}
	return typ
}

// bashPPBindLocalSelector resolves x.a.b.M against the static field graph
// rooted at x, then performs the final bind against x's live storage. Keeping
// the entire edge path is what preserves addressability and nil diagnostics;
// materializing intermediate shell values would lose both.
func (r *Runner) bashPPBindLocalSelector(c *syntax.BashPPCall, root *bashPPCell) (*bashPPFunc, bool) {
	method := c.Fun[len(c.Fun)-1].Value
	// The interface test and the payload it binds come from one snapshot;
	// root itself stays live because the field path below binds against the
	// receiver's own storage.
	if rootView := root.view(); len(c.Fun) == 2 && rootView.interfaceValue != nil {
		oldPos := r.curStmtPos
		if r.bashPPGoSource {
			r.curStmtPos = c.Fun[1].Pos()
		}
		defer func() { r.curStmtPos = oldPos }()
		return r.bashPPBindInterfaceMethod(rootView.interfaceValue, method)
	}
	typ := bashPPSelectorCellType(root)
	if typ == nil {
		r.errf("BASHPP-ESELECTOR-TYPE: local %s has no typed selector path\n", c.Fun[0].Value)
		r.exit.code = 2
		return nil, false
	}
	var edges []bashPPEmbedEdge
	for _, part := range c.Fun[1 : len(c.Fun)-1] {
		sel := r.bashPPResolveField(typ, part.Value)
		if sel.ambiguous {
			r.errf("BASHPP-ESELECTOR-AMBIGUOUS: ambiguous selector %s.%s\n", bashPPTypeText(typ), part.Value)
			r.exit.code = 2
			return nil, false
		}
		if len(sel.edges) == 0 {
			r.errf("BASHPP-ESELECTOR-UNKNOWN: %s has no field %q\n", bashPPTypeText(typ), part.Value)
			r.exit.code = 2
			return nil, false
		}
		edges = append(edges, sel.edges...)
		typ = sel.fieldType
	}
	sel := r.bashPPResolveSelection(typ, method, true, true)
	if sel.ambiguous {
		r.errf("BASHPP-ESELECTOR-AMBIGUOUS: ambiguous selector %s.%s\n", bashPPTypeText(typ), method)
		r.exit.code = 2
		return nil, false
	}
	if sel.method == nil && sel.interfaceSpec == nil {
		if fn, handled := r.bashPPBindFuncField(root, edges, sel); handled {
			return fn, fn != nil
		}
		if fn, handled := r.goSourceBindImportedScalarMethod(root, edges, typ, method); handled {
			return fn, fn != nil
		}
		r.errf("type %s has no method %s\n", bashPPTypeText(typ), method)
		r.exit.code = 2
		return nil, false
	}
	sel.edges = append(edges, sel.edges...)
	return r.bashPPBindPromotedMethod(root, method, sel, true)
}

// bashPPBindFuncField dispatches a selector call whose final component names a
// func-typed struct field rather than a method: `t.f(a, b)` for `type testCase
// struct{ f fn }`. Only a selection that resolved to a field whose type is a
// func type is dispatched, so an unknown name keeps the method diagnostic.
// A nil field value is Go's nil-function call: a run-time panic at call time.
func (r *Runner) bashPPBindFuncField(root *bashPPCell, path []bashPPEmbedEdge, sel bashPPSelection) (*bashPPFunc, bool) {
	if sel.fieldType == nil || len(sel.edges) == 0 {
		return nil, false
	}
	if _, ok := r.bashPPUnderlyingType(sel.fieldType).(*syntax.BashPPFuncType); !ok {
		return nil, false
	}
	value, meta, err := r.bashPPReadCellValue(root)
	if err == nil {
		edges := append(append([]bashPPEmbedEdge(nil), path...), sel.edges...)
		value, _, err = bashPPReadSelection(value, meta, edges)
	}
	if err != nil {
		r.goSourceRuntimeFault(err)
		return nil, true
	}
	switch v := value.(type) {
	case string:
		if fn, ok := r.bashPPClosure(v); ok {
			return fn, true
		}
		if v != "" && v != "nil" {
			// A non-empty value that is not a live closure handle is a shape
			// this dispatch does not understand; keep the method diagnostic.
			return nil, false
		}
	case *bashPPBridgeValue:
		// A zero-valued func field is stored as a typed nil bridge value; see
		// [Runner.goSourceNilCallableOrChannel].
		if v != nil && v.Kind != "nil" {
			return nil, false
		}
	case nil:
	default:
		return nil, false
	}
	r.bashPPRaiseNilFuncCall()
	return nil, true
}

func (r *Runner) bashPPInstantiateFunc(c *syntax.BashPPCall, fn *bashPPFunc) (*bashPPFunc, bool) {
	params := fn.typeParams()
	// A function value that was already instantiated — by the context it was
	// bound in, see [Runner.bashPPContextualFuncValue] — is called at the
	// types it carries. Re-inferring them from this call's arguments would ask
	// a `func() int` value to determine `P` from no arguments at all.
	if len(c.TypeArgs) == 0 && bashPPFullyInstantiated(fn, params) {
		return fn, true
	}
	if len(params) == 0 {
		if len(c.TypeArgs) > 0 {
			r.errf("BASHPP-EGENERIC-ARITY: %s is not generic; got %d type argument(s)\n", fn.name(), len(c.TypeArgs))
			r.exit.code = 2
			return nil, false
		}
		return fn, true
	}
	want := bashPPTypeParamCount(params)
	if len(c.TypeArgs) > 0 && len(c.TypeArgs) != want {
		r.errf("BASHPP-EGENERIC-ARITY: %s expects %d type argument(s); got %d\n", fn.name(), want, len(c.TypeArgs))
		r.exit.code = 2
		return nil, false
	}
	bindings := make(map[string]syntax.BashPPTypeExpr, want)
	if len(c.TypeArgs) > 0 {
		if err := bashPPValidateConcreteTypeArgs(c.TypeArgs); err != nil {
			r.errf("%v\n", err)
			r.exit.code = 2
			return nil, false
		}
		i := 0
		for _, group := range params {
			for _, name := range group.Names {
				// `g[T]()` inside a generic body names the caller's own type
				// parameter; the instantiation binds what this frame bound it
				// to, exactly as a type spelled in the body resolves.
				bindings[name.Value] = r.bashPPBindTypeExpr(c.TypeArgs[i].ArgType)
				i++
			}
		}
	} else if !r.bashPPInferTypeArgs(c, fn, bindings) {
		return nil, false
	}
	// Blank parameters share one binding slot, so the count of bindings is
	// the count of distinct names.
	if len(bindings) != bashPPDistinctTypeParamCount(params) {
		r.errf("BASHPP-EGENERIC-INFER: cannot infer type arguments for %s\n", fn.name())
		r.exit.code = 2
		return nil, false
	}
	if !r.bashPPCheckTypeConstraints(fn, params, bindings) {
		return nil, false
	}
	bound := *fn
	// A generic METHOD arrives here already carrying the receiver's bindings,
	// which the binding step read off the receiver value. Those names are
	// disjoint from the method's own — the parser rejects the collision — so
	// the two scopes merge rather than replace, and a signature mentioning
	// both substitutes both.
	if len(fn.typeArgs) > 0 {
		merged := make(map[string]syntax.BashPPTypeExpr, len(fn.typeArgs)+len(bindings))
		maps.Copy(merged, fn.typeArgs)
		maps.Copy(merged, bindings)
		bindings = merged
	}
	bound.typeArgs = bindings
	return &bound, true
}

func bashPPDistinctTypeParamCount(params []*syntax.BashPPTypeParam) int {
	seen := make(map[string]bool)
	for _, param := range params {
		for _, name := range param.Names {
			seen[name.Value] = true
		}
	}
	return len(seen)
}

func bashPPTypeParamCount(params []*syntax.BashPPTypeParam) int {
	var n int
	for _, param := range params {
		n += len(param.Names)
	}
	return n
}

func (r *Runner) bashPPInferTypeArgs(c *syntax.BashPPCall, fn *bashPPFunc, bindings map[string]syntax.BashPPTypeExpr) bool {
	// fn.params() rather than fn.decl.Params: on a generic method the receiver
	// type parameters are already bound, so substituting them first leaves
	// only the method's own parameters open to inference. A parameter typed
	// with a receiver parameter must therefore MATCH its receiver binding
	// instead of rebinding it.
	params := bashppParams(fn.params())
	args := c.Args
	if fn.skipArgs > 0 && len(args) >= fn.skipArgs {
		args = args[fn.skipArgs:]
	}
	for i, arg := range args {
		if i >= len(params) {
			break
		}
		actual := r.bashPPTypeOfArg(arg)
		if actual == nil {
			continue
		}
		if !r.bashPPInferTypeFromParam(params[i].typ, actual, bindings) {
			r.errf("BASHPP-EGENERIC-INFER: conflicting type inference for %s\n", fn.name())
			r.exit.code = 2
			return false
		}
	}
	return true
}

func (r *Runner) bashPPTypeOfArg(w *syntax.Word) syntax.BashPPTypeExpr {
	if cell := r.bashPPCellForWord(w); cell != nil {
		if cell.declType != nil {
			return cell.declType
		}
		if meta := bashPPCellMeta(cell); meta != nil && meta.typ != nil {
			return meta.typ
		}
		if cell.typeName != "" {
			name := &syntax.Lit{ValuePos: w.Pos(), ValueEnd: w.End(), Value: cell.typeName}
			typ := syntax.BashPPTypeExpr(&syntax.BashPPNamedType{Name: name})
			if cell.pointer {
				return &syntax.BashPPPointerType{Star: w.Pos(), Element: typ}
			}
			return typ
		}
	}
	value := r.bashPPExprValue(w)
	switch {
	case r.bashPPValueFits("int", value):
		return &syntax.BashPPNamedType{Name: &syntax.Lit{ValuePos: w.Pos(), ValueEnd: w.End(), Value: "int"}}
	case r.bashPPValueFits("bool", value):
		return &syntax.BashPPNamedType{Name: &syntax.Lit{ValuePos: w.Pos(), ValueEnd: w.End(), Value: "bool"}}
	default:
		return &syntax.BashPPNamedType{Name: &syntax.Lit{ValuePos: w.Pos(), ValueEnd: w.End(), Value: "string"}}
	}
}

func (r *Runner) bashPPInferTypeFromParam(param, actual syntax.BashPPTypeExpr, bindings map[string]syntax.BashPPTypeExpr) bool {
	switch p := param.(type) {
	case *syntax.BashPPTypeParamType:
		if prev := bindings[p.Name.Value]; prev != nil {
			return r.bashPPTypeAssignable(actual, prev) && r.bashPPTypeAssignable(prev, actual)
		}
		bindings[p.Name.Value] = actual
		return true
	case *syntax.BashPPPointerType:
		a, ok := actual.(*syntax.BashPPPointerType)
		return ok && r.bashPPInferTypeFromParam(p.Element, a.Element, bindings)
	case *syntax.BashPPCollectionType:
		a, ok := actual.(*syntax.BashPPCollectionType)
		if !ok || p.Kind != a.Kind {
			return false
		}
		if p.Kind == "map" && !r.bashPPInferTypeFromParam(p.Key, a.Key, bindings) {
			return false
		}
		return r.bashPPInferTypeFromParam(p.Element, a.Element, bindings)
	}
	return true
}

func (r *Runner) bashPPCheckTypeConstraints(fn *bashPPFunc, params []*syntax.BashPPTypeParam, bindings map[string]syntax.BashPPTypeExpr) bool {
	constraintBindings := bindings
	if len(fn.typeArgs) > 0 {
		constraintBindings = make(map[string]syntax.BashPPTypeExpr, len(fn.typeArgs)+len(bindings))
		maps.Copy(constraintBindings, fn.typeArgs)
		maps.Copy(constraintBindings, bindings)
	}
	for _, group := range params {
		constraint := bashPPSubstituteType(group.Constraint, constraintBindings)
		for _, name := range group.Names {
			arg := bindings[name.Value]
			if arg == nil {
				continue
			}
			if r.bashPPConstraintSatisfied(arg, constraint) {
				continue
			}
			r.errf("BASHPP-EGENERIC-CONSTRAINT: %s does not satisfy constraint for %s in %s\n", bashPPTypeText(arg), name.Value, fn.name())
			r.exit.code = 2
			return false
		}
	}
	return true
}

func (r *Runner) bashPPTypeSetSatisfied(arg, constraint syntax.BashPPTypeExpr) bool {
	switch c := constraint.(type) {
	case *syntax.BashPPNamedType:
		if c.Name.Value == "comparable" {
			return r.bashPPComparableType(arg, make(map[string]bool))
		}
		// A union term may itself be an interface — `OrderedNumeric |
		// Complex` — whose type set, not its assignability, decides.
		if iface, ok := r.bashPPInterfaceType(c); ok {
			return r.bashPPConstraintSatisfied(arg, iface)
		}
		// A declared constraint such as `type C comparable` carries the type
		// set of comparable. Do not generally unwrap named types here: a term
		// `MyInt` admits MyInt only, not every type whose underlying type is int.
		if r.bashPPNamedComparableConstraint(c, make(map[string]bool)) {
			return r.bashPPComparableType(arg, make(map[string]bool))
		}
		return r.bashPPTypeAssignable(arg, c)
	case *syntax.BashPPInterfaceType:
		return r.bashPPConstraintSatisfied(arg, c)
	case *syntax.BashPPUnionType:
		for _, term := range c.Terms {
			if r.bashPPTypeSetSatisfied(arg, term) {
				return true
			}
		}
		return false
	case *syntax.BashPPApproxType:
		return bashPPTypeText(r.bashPPUnderlyingType(arg)) == bashPPTypeText(r.bashPPUnderlyingType(c.Term))
	default:
		return r.bashPPTypeAssignable(arg, c)
	}
}

// bashPPNamedComparableConstraint reports whether a declared named constraint
// resolves specifically to the predeclared comparable constraint. General
// named terms retain their identity and must not be unwrapped by the type-set
// matcher.
func (r *Runner) bashPPNamedComparableConstraint(named *syntax.BashPPNamedType, seen map[string]bool) bool {
	if named == nil || named.Name == nil {
		return false
	}
	name := named.Name.Value
	if name == "comparable" {
		return true
	}
	if seen[name] {
		return false
	}
	seen[name] = true
	decl, ok := r.bashPPTypes[name]
	if !ok || decl.typeExpr == nil {
		return false
	}
	next, ok := r.bashPPInstantiateNamedType(named).(*syntax.BashPPNamedType)
	return ok && r.bashPPNamedComparableConstraint(next, seen)
}

func (r *Runner) bashPPComparableType(typ syntax.BashPPTypeExpr, seen map[string]bool) bool {
	switch x := typ.(type) {
	case *syntax.BashPPNamedType:
		name := x.Name.Value
		// unsafe.Pointer is a pointer type: always comparable.
		if r.goSourceUnsafePointerType(x) {
			return true
		}
		decl, found := r.bashPPTypes[name]
		if !found {
			if bashPPBuiltinType(name) {
				return true
			}
			// Since Go 1.20, an ordinary interface may satisfy comparable even
			// though comparing two values of that interface can still panic when
			// their identical dynamic type is not comparable. Dependency-owned
			// interfaces have no interpreter declaration, so consult their
			// authenticated go/types export metadata. IsMethodSet identifies the
			// basic interfaces which may be used as ordinary value types; a
			// constraint interface with type terms cannot enter through here.
			if r.bashPPImportedBasicInterface(x) {
				return true
			}
			// A dependency-owned defined type has no interpreter declaration;
			// its identity lives in the import's export metadata. One with a
			// basic underlying type is comparable, and it is exactly the shape
			// bashPPSprint165MapKey can hash by that underlying kind.
			_, imported := r.goSourceImportedScalarUnderlying(name)
			return imported
		}
		if seen[bashPPTypeText(x)] || decl.typeExpr == nil {
			return false
		}
		seen[bashPPTypeText(x)] = true
		defer delete(seen, bashPPTypeText(x))
		return r.bashPPComparableType(r.bashPPInstantiateNamedType(x), seen)
	case *syntax.BashPPPointerType:
		return true
	case *syntax.BashPPCollectionType:
		return x.Kind == "array" && r.bashPPComparableType(x.Element, seen)
	case *syntax.BashPPStructType:
		for _, field := range x.Fields {
			if field.FieldTypeExpr == nil || !r.bashPPComparableType(field.FieldTypeExpr, seen) {
				return false
			}
		}
		return true
	case *syntax.BashPPInterfaceType:
		return true
	case *syntax.BashPPTypeParamType:
		return true
	}
	return false
}

func (r *Runner) bashPPImportedBasicInterface(typ *syntax.BashPPNamedType) bool {
	if !r.bashPPGoSource {
		return false
	}
	native := r.bashPPEmbeddedNativeType(typ)
	if native == nil {
		return false
	}
	iface, ok := types.Unalias(native).Underlying().(*types.Interface)
	return ok && iface.IsMethodSet()
}

func (r *Runner) bashPPInstantiateNamedType(named *syntax.BashPPNamedType) syntax.BashPPTypeExpr {
	decl, ok := r.bashPPTypes[named.Name.Value]
	if !ok {
		return named
	}
	return bashPPInstantiateTypeDeclaration(named, decl)
}

func bashPPInstantiateTypeDeclaration(named *syntax.BashPPNamedType, decl bashPPType) syntax.BashPPTypeExpr {
	if len(decl.typeParams) == 0 {
		if decl.typeExpr != nil {
			return decl.typeExpr
		}
		return named
	}
	want := bashPPTypeParamCount(decl.typeParams)
	if len(named.TypeArgs) != want {
		return named
	}
	bindings := make(map[string]syntax.BashPPTypeExpr, want)
	i := 0
	for _, group := range decl.typeParams {
		for _, name := range group.Names {
			bindings[name.Value] = named.TypeArgs[i].ArgType
			i++
		}
	}
	return bashPPSubstituteType(decl.typeExpr, bindings)
}

func (r *Runner) bashPPValidateNamedTypeArgs(named *syntax.BashPPNamedType) error {
	decl, ok := r.bashPPTypes[named.Name.Value]
	if !ok {
		return nil
	}
	want := bashPPTypeParamCount(decl.typeParams)
	if want == 0 {
		if len(named.TypeArgs) > 0 {
			return fmt.Errorf("BASHPP-EGENERIC-ARITY: %s is not generic; got %d type argument(s)", named.Name.Value, len(named.TypeArgs))
		}
		return nil
	}
	if len(named.TypeArgs) != want {
		return fmt.Errorf("BASHPP-EGENERIC-ARITY: %s expects %d type argument(s); got %d", named.Name.Value, want, len(named.TypeArgs))
	}
	if err := bashPPValidateConcreteTypeArgs(named.TypeArgs); err != nil {
		return err
	}
	bindings := make(map[string]syntax.BashPPTypeExpr, want)
	i := 0
	for _, group := range decl.typeParams {
		for _, param := range group.Names {
			bindings[param.Value] = named.TypeArgs[i].ArgType
			i++
		}
	}
	i = 0
	for _, group := range decl.typeParams {
		constraint := bashPPSubstituteType(group.Constraint, bindings)
		for _, param := range group.Names {
			arg := named.TypeArgs[i].ArgType
			i++
			// `type List[T Ordered] struct { next *List[T] }`: an argument
			// that is itself a type parameter is checked where the outer
			// declaration is instantiated, the first point a concrete type
			// exists; go/types has already checked the parameter's own
			// constraint implies this one.
			if r.bashPPOpenTypeParam(arg) {
				continue
			}
			if !r.bashPPConstraintSatisfied(arg, constraint) {
				return fmt.Errorf("BASHPP-EGENERIC-CONSTRAINT: %s does not satisfy constraint for %s in %s", bashPPTypeText(arg), param.Value, named.Name.Value)
			}
		}
	}
	return nil
}

// bashPPOpenTypeParam reports whether typ is a type parameter the current
// frame has not bound: the marker the parser leaves in signature positions,
// or a bare name that is neither declared nor builtin while a generic
// declaration is being validated.
func (r *Runner) bashPPOpenTypeParam(typ syntax.BashPPTypeExpr) bool {
	switch x := typ.(type) {
	case *syntax.BashPPTypeParamType:
		return r.bashPPTypeParamArgs[x.Name.Value] == nil
	case *syntax.BashPPPointerType:
		return r.bashPPOpenTypeParam(x.Element)
	case *syntax.BashPPNamedType:
		if x.Name == nil || len(x.TypeArgs) > 0 {
			return false
		}
		if _, declared := r.bashPPTypes[x.Name.Value]; declared || bashPPBuiltinType(x.Name.Value) {
			return false
		}
		return r.bashPPTypeParamArgs[x.Name.Value] == nil
	}
	return false
}

func bashPPValidateConcreteTypeArgs(args []*syntax.BashPPTypeArg) error {
	for _, arg := range args {
		if arg == nil || arg.ArgType == nil {
			return fmt.Errorf("BASHPP-EGENERIC-ARG: missing concrete type argument")
		}
		if err := bashPPValidateConcreteTypeExpr(arg.ArgType); err != nil {
			return err
		}
	}
	return nil
}

func bashPPValidateConcreteTypeExpr(typ syntax.BashPPTypeExpr) error {
	switch x := typ.(type) {
	case *syntax.BashPPUnionType, *syntax.BashPPApproxType:
		return fmt.Errorf("BASHPP-EGENERIC-ARG: %s is a constraint expression, not a concrete type", bashPPTypeText(typ))
	case *syntax.BashPPNamedType:
		return bashPPValidateConcreteTypeArgs(x.TypeArgs)
	case *syntax.BashPPCollectionType:
		if x.Key != nil {
			if err := bashPPValidateConcreteTypeExpr(x.Key); err != nil {
				return err
			}
		}
		if x.Element != nil {
			return bashPPValidateConcreteTypeExpr(x.Element)
		}
	case *syntax.BashPPPointerType:
		if x.Element != nil {
			return bashPPValidateConcreteTypeExpr(x.Element)
		}
	case *syntax.BashPPStructType:
		for _, field := range x.Fields {
			if err := bashPPValidateConcreteTypeExpr(field.FieldTypeExpr); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *Runner) bashPPConstraintSatisfied(arg, constraint syntax.BashPPTypeExpr) bool {
	// `any` is satisfied by every type, so there is nothing to decide and no
	// need for the argument to be a concrete type. gosource lowers `any` to a
	// literal empty interface, which would otherwise be routed through the
	// implements check and reject an uninstantiated type parameter — the form
	// a generic type takes inside its own declaration, as in the Tour's
	// `type List[T any] struct { next *List[T]; val T }`. A type parameter
	// used as a type argument is checked against a real constraint where the
	// generic type is instantiated, which is the first point a concrete type
	// exists to check.
	if bashPPEmptyInterfaceType(constraint) {
		return true
	}
	switch c := constraint.(type) {
	case *syntax.BashPPNamedType:
		switch c.Name.Value {
		case "any":
			return true
		case "comparable":
			return r.bashPPComparableType(arg, make(map[string]bool))
		default:
			if iface, ok := r.bashPPInterfaceType(c); ok {
				return r.bashPPImplements(arg, iface) == nil && r.bashPPInterfaceTypeSetSatisfied(arg, iface, make(map[*syntax.BashPPInterfaceType]bool))
			}
			if r.bashPPNamedComparableConstraint(c, make(map[string]bool)) {
				return r.bashPPComparableType(arg, make(map[string]bool))
			}
			return r.bashPPTypeAssignable(arg, c)
		}
	case *syntax.BashPPInterfaceType:
		return r.bashPPImplements(arg, c) == nil && r.bashPPInterfaceTypeSetSatisfied(arg, c, make(map[*syntax.BashPPInterfaceType]bool))
	case *syntax.BashPPUnionType, *syntax.BashPPApproxType:
		return r.bashPPTypeSetSatisfied(arg, constraint)
	}
	// Any other type written as a constraint — `T []MyByte`, `P *S` — is Go's
	// shorthand for the interface with that one type term.
	return r.bashPPTypeSetSatisfied(arg, constraint)
}

// bashPPEmptyInterfaceType reports whether typ is spelled as an interface with
// no methods and no type terms, that is, `any`.
func bashPPEmptyInterfaceType(typ syntax.BashPPTypeExpr) bool {
	iface, ok := typ.(*syntax.BashPPInterfaceType)
	return ok && len(iface.Elems) == 0 && len(iface.Methods) == 0
}

func (r *Runner) bashPPCellForWord(w *syntax.Word) *bashPPCell {
	if w == nil || len(w.Parts) != 1 || r.bashPPScope == nil {
		return nil
	}
	lit, ok := w.Parts[0].(*syntax.Lit)
	if !ok || !syntax.BashPPValidIdent(lit.Value) {
		return nil
	}
	return r.bashPPScope.lookup(lit.Value)
}

// bashPPPointerCell wraps a pointer value in an anonymous cell. `&x`, `&T{…}`
// and `new(T)` name storage that no variable holds, so there is no cell to
// carry their provenance to a parameter; this makes one.
func bashPPPointerCell(ptr *bashPPPointer) *bashPPCell {
	// A pointer variable's own shell binding is a set, empty string: the
	// pointer travels in pointerValue, not in the text. An anonymous pointer
	// cell must look the same, or reading it reports an unset name.
	vr := expand.Variable{Set: true, Kind: expand.String}
	if ptr == nil {
		return &bashPPCell{pointer: true, nilPointer: true, vr: vr}
	}
	cell := &bashPPCell{pointer: true, pointerValue: ptr, vr: vr, declType: &syntax.BashPPPointerType{Element: ptr.elem}}
	if named, ok := ptr.elem.(*syntax.BashPPNamedType); ok && named.Name != nil {
		cell.typeName = named.Name.Value
	}
	return cell
}

// bashPPStructuredArgCell reports the provenance cell of a call argument that
// has no scalar form — a struct, collection, pointer or interface value, plus
// the `&x` / `&T{…}` / `new(T)` and composite-literal spellings that produce
// one without naming a variable. A scalar argument returns (nil, nil) so it
// keeps being evaluated as an expression rather than passed by name.
func (r *Runner) bashPPStructuredArgCell(w *syntax.Word, expr syntax.BashPPExpr) (*bashPPCell, error) {
	if r.bashPPGoSource && bashPPRecoverExpr(expr) && r.bashPPFuncs["recover"] == nil && (r.bashPPScope == nil || r.bashPPScope.lookup("recover") == nil) {
		iv, _ := r.bashPPRecoverInterfaceValue()
		if iv.nilIface {
			return &bashPPCell{vr: expand.Variable{Set: true, Kind: expand.String}, declType: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "any"}}, interfaceValue: iv}, nil
		}
		return &bashPPCell{vr: iv.cell.vr, declType: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "any"}}, interfaceValue: iv}, nil
	}
	if cell, handled, err := r.goSourceCallableCell(expr); handled {
		return cell, err
	}
	switch x := expr.(type) {
	case *syntax.BashPPAddressExpr:
		ptr, err := r.bashPPPointerExprValue(expr)
		if err != nil {
			if native, ok := r.goSourceNativePointerError(err); ok {
				return r.goSourceNativeValueCell(*native), nil
			}
			return nil, err
		}
		return bashPPPointerCell(ptr), nil
	case *syntax.BashPPNewExpr:
		ptr, err := r.bashPPPointerExprValue(expr)
		if err != nil {
			return nil, err
		}
		return bashPPPointerCell(ptr), nil
	case *syntax.BashPPConvertExpr:
		// `f((*T)(p))`: a pointer conversion travels as the retyped pointer.
		if ptr, target, converted, err := r.bashPPPointerConversion(x); converted {
			if err != nil {
				return nil, err
			}
			cell := bashPPPointerCell(ptr)
			cell.declType = target
			return cell, nil
		}
		// `f(unsafe.Pointer(new(T)))`: likewise the pointer it retypes.
		if cell, handled, err := r.goSourceUnsafePointerCell(x); handled {
			return cell, err
		}
		// `Stringer(m).String()`, `f(any(x))`, `return I(v)`: a conversion
		// to an interface is the interface value that boxes its operand.
		if cell, handled, err := r.bashPPInterfaceConversion(x); handled {
			return cell, err
		}
		// `f([]byte(s))`: a conversion whose result is a collection travels as
		// the cell holding it; see bashPPConvertCollectionCell in
		// bashpp_collection_convert.go. Scalar conversions report false.
		cell, handled, err := r.bashPPConvertCollectionCell(x)
		if !handled {
			cell, handled, err = r.goSourceConvertedCompositeCell(x)
		}
		if !handled {
			return nil, nil
		}
		return cell, err
	case *syntax.BashPPCompositeLit:
		if x.LitType == nil {
			return nil, nil
		}
		if r.bashPPNativeType(x.LitType) {
			value, err := r.bashPPNativeComposite(x, false)
			if err != nil {
				return nil, err
			}
			return r.goSourceNativeValueCell(value), nil
		}
		value, meta, err := r.bashPPEvalComposite(x, nil)
		if err != nil {
			return nil, err
		}
		cell := &bashPPCell{declType: x.LitType}
		if named, ok := x.LitType.(*syntax.BashPPNamedType); ok && named.Name != nil {
			cell.typeName = named.Name.Value
		}
		bashPPStoreCellValue(cell, value, meta)
		if carrier, ok := r.bashPPGoSourceCollectionCarrier(value, meta); ok {
			cell.vr = carrier
		}
		return cell, nil
	case *syntax.BashPPTypeAssertExpr:
		// `return v.(I)`, `f(v.(T))`: an assertion yields the asserted cell —
		// an interface value with its dynamic type, or the concrete value —
		// which the scalar evaluator would flatten to text and a bare type
		// name.
		cell, handled, err := r.bashPPAssertCandidate(x)
		if !handled {
			return nil, nil
		}
		return cell, err
	case *syntax.BashPPDerefExpr, *syntax.BashPPIndexExpr, *syntax.BashPPSelectorExpr, *syntax.BashPPSliceExpr:
		// `f(*p)`, `f(xs[0])`, `f(v.Inner)`, `f(xs[1:])`: a read that yields structured
		// storage is passed as the value it is. A scalar read has no metadata
		// and is left to the scalar evaluator, which also owns the diagnostic
		// when the read itself fails.
		value, meta, err := r.bashPPReadExpr(expr)
		if err != nil || meta == nil {
			return nil, nil
		}
		cell := &bashPPCell{declType: meta.typ}
		if named, ok := meta.typ.(*syntax.BashPPNamedType); ok && named.Name != nil {
			cell.typeName = named.Name.Value
		}
		bashPPStoreCellValue(cell, value, meta)
		if carrier, ok := r.bashPPGoSourceCollectionCarrier(value, meta); ok {
			cell.vr = carrier
		}
		return cell, nil
	}
	if id, ok := expr.(*syntax.BashPPIdent); ok && r.bashPPScope != nil {
		if cell := r.bashPPScope.lookup(id.Name.Value); bashPPStructuredCell(cell) || r.bashPPFuncTypedCell(cell) {
			return cell, nil
		}
		return nil, nil
	}
	if cell := r.bashPPCellForWord(w); bashPPStructuredCell(cell) {
		return cell, nil
	}
	return nil, nil
}

// bashPPStructuredCell reports whether a binding holds a value with no scalar
// spelling, so that passing or returning it has to carry the cell itself.
func bashPPStructuredCell(cell *bashPPCell) bool {
	cell = cell.view()
	return cell != nil && (cell.pointer || cell.interfaceValue != nil || cell.vr.Kind == expand.Object)
}

// bashPPCellForArg resolves the provenance cell of one call argument. A named
// operand is the variable itself, so a pointer argument keeps its identity.
// `&x` and `new(T)` name freshly taken storage that no variable holds; the
// pointer is wrapped in an anonymous cell so it travels the same parameter
// binding path a named pointer does, instead of arriving as a nil pointer.
func (r *Runner) bashPPCellForArg(w *syntax.Word, expr syntax.BashPPExpr) *bashPPCell {
	if cell := r.bashPPCellForWord(w); cell != nil {
		return cell
	}
	switch expr.(type) {
	case *syntax.BashPPAddressExpr, *syntax.BashPPNewExpr:
	default:
		return nil
	}
	ptr, err := r.bashPPPointerExprValue(expr)
	if err != nil {
		r.errf("%s%v\n", r.bashErrPrefix(expr.Pos()), err)
		r.exit = exitStatus{code: 2}
		return nil
	}
	if ptr == nil {
		return nil
	}
	return bashPPPointerCell(ptr)
}

// bashPPGoSourceArgCell gives a Go-source argument that names no variable its
// own provenance cell. A Go call's argument is an expression, so `do(21)` has
// to arrive carrying the kind and type of 21 — without that the callee sees
// only text, and an interface parameter has no dynamic type to switch on.
// Classic Bash++ calls are untouched: there a bare word is its own literal.
func (r *Runner) bashPPGoSourceArgCell(w *syntax.Word, expr syntax.BashPPExpr) *bashPPCell {
	if !r.bashPPGoSource || expr == nil {
		return nil
	}
	if structured, err := r.bashPPStructuredArgCell(w, expr); err == nil && structured != nil {
		return structured
	}
	value, err := r.bashPPEvalScalarExpr(expr)
	if err != nil || value.kind() == constant.Unknown {
		return nil
	}
	cell := &bashPPCell{
		vr:                  expand.Variable{Set: true, Kind: expand.String, Str: bashPPScalarStorageString(value)},
		scalarKind:          value.kind(),
		negativeZero:        value.negativeZero,
		nonFinite:           value.nonFinite,
		hasNonFinite:        value.hasNonFinite,
		nonFiniteComplex:    value.nonFiniteComplex,
		hasNonFiniteComplex: value.hasNonFiniteComplex,
	}
	if value.typ != "" {
		cell.declType, cell.typeName = bashPPScalarNamedType(value.typ)
	}
	return cell
}

// bashPPTypedCallArgs binds the positioned Go-form arguments of a call whose
// result is consumed as a value. Each argument is evaluated once: a structured
// operand travels through its provenance cell, everything else through the
// scalar evaluator, so `Abs(v)` on a struct and `Abs(x*2)` on a number both
// reach the callee instead of the second form forcing the first to be refused
// as "not a scalar".
func (r *Runner) bashPPTypedCallArgs(call *syntax.BashPPCall, fn *bashPPFunc) (result []string, success bool, failure error) {
	defer func() {
		if success && !r.goSourceFinalizeReceiver(fn) {
			result = nil
			success = false
			failure = errBashPPScalarInterrupted
		}
	}()
	// The Go-source argument path retains a spread slice as one cell and marks
	// the call as spread.  Expanding it here would turn []T into its printed
	// form before goSourceBindSpreadVariadic can preserve its backing store.
	if r.bashPPGoSource {
		return r.goSourceCallArguments(call, fn)
	}
	if len(call.ArgExprs) != len(call.Args) {
		return nil, false, fmt.Errorf("BASHPP-EEXPR-CALL: inconsistent positioned scalar arguments")
	}
	args := make([]string, len(call.ArgExprs))
	cells := make([]*bashPPCell, len(call.ArgExprs))
	channels := make([]*bashPPChannel, len(call.ArgExprs))
	interfaces := make([]*bashPPInterfaceValue, len(call.ArgExprs))
	for i, expr := range call.ArgExprs {
		if r.bashPPGoSource {
			if cell, handled, err := r.goSourceChannelValueCell(expr); handled {
				if err != nil {
					return nil, false, err
				}
				if cell.channel != nil && cell.channelOwner != r.bashPPConcurrent {
					return nil, false, fmt.Errorf("channel belongs to another task group")
				}
				channels[i], cells[i], args[i] = cell.channel, cell, cell.vr.String()
				continue
			}
		}
		structured, err := r.bashPPStructuredArgCell(call.Args[i], expr)
		if err != nil {
			return nil, false, err
		}
		if structured != nil {
			copied := bashPPCopyAssignmentCell(structured)
			// A value copy never carries direct channel authority; that is
			// restored only from separately checked owner provenance.
			copied.channel, copied.channelOwner = nil, nil
			cells[i], interfaces[i] = copied, copied.interfaceValue
			args[i] = r.bashPPExprValue(call.Args[i])
			if _, ok := r.bashPPClosure(copied.vr.Str); ok {
				args[i] = copied.vr.Str
			}
			continue
		}
		value, err := r.bashPPEvalScalarExpr(expr)
		if err != nil {
			return nil, false, err
		}
		text := bashPPScalarStorageString(value)
		cell := &bashPPCell{vr: expand.Variable{Set: true, Kind: expand.String, Str: text}, scalarKind: value.kind(), negativeZero: value.negativeZero, nonFinite: value.nonFinite, hasNonFinite: value.hasNonFinite, nonFiniteComplex: value.nonFiniteComplex, hasNonFiniteComplex: value.hasNonFiniteComplex}
		if value.typ != "" {
			cell.declType, cell.typeName = bashPPScalarNamedType(value.typ)
		}
		cells[i], args[i] = cell, text
	}
	r.bashPPCallInterfaces = interfaces
	bound, ok := r.bashPPBindCall(fn, args, channels, cells, nil, len(args))
	return bound, ok, nil
}

func (r *Runner) bashPPBindInterfaceMethod(iv *bashPPInterfaceValue, method string) (*bashPPFunc, bool) {
	if iv == nil || iv.nilIface {
		// Selecting a method on a nil interface is Go's nil dereference:
		// the itab lookup faults.
		if r.bashPPGoSource {
			r.goSourceRuntimeFault(errBashPPNilDereference)
			return nil, false
		}
		r.errf("nil interface has no method %s\n", method)
		r.exit.code = 2
		return nil, false
	}
	if iv.cell == nil {
		r.errf("interface value has no dynamic receiver for method %s\n", method)
		r.exit.code = 2
		return nil, false
	}
	if fn, ok := bashPPForeignErrorMethod(iv, method); ok {
		return fn, true
	}
	if bashPPRuntimeErrorType(iv.dynamic) {
		return r.bashPPRuntimeErrorMethod(iv, method)
	}
	// A dependency-owned dynamic value binds its method there; see
	// bashpp_sprint165_runtime_panic.go.
	if native, claimed, err := r.goSourceNativeReceiverMethod(iv.cell, method); claimed {
		if err != nil {
			r.exit.fatal(err)
			return nil, false
		}
		return native, true
	}
	if receiver, ambiguous := r.bashPPPromotedNativeCellReceiver(iv.cell, iv.dynamic, method); ambiguous {
		r.errf("BASHPP-ESELECTOR-AMBIGUOUS: ambiguous selector %s.%s\n", bashPPTypeText(iv.dynamic), method)
		r.exit.code = 2
		return nil, false
	} else if receiver != nil {
		bound, err := r.bashPPBindNativeMethod(r.ectx, *receiver, method)
		if err != nil {
			r.exit.fatal(err)
			return nil, false
		}
		fn := &bashPPFunc{native: &bound}
		if sig := syntax.BashPPTypeExprFromText(bound.Type); sig != nil {
			if ft, ok := sig.(*syntax.BashPPFuncType); ok {
				fn.lit = &syntax.BashPPFuncLit{Params: ft.Params, Results: ft.Results}
			}
		}
		return fn, true
	}
	sel := r.bashPPResolveSelection(iv.dynamic, method, true, false)
	if sel.ambiguous {
		r.errf("BASHPP-ESELECTOR-AMBIGUOUS: ambiguous selector %s.%s\n", bashPPTypeText(iv.dynamic), method)
		r.exit.code = 2
		return nil, false
	}
	if sel.method == nil && sel.interfaceSpec == nil {
		r.errf("type %s has no method %s\n", bashPPTypeText(iv.dynamic), method)
		r.exit.code = 2
		return nil, false
	}
	if wrapper := r.goSourcePanicWrap(iv, sel, method); wrapper != nil {
		return wrapper, true
	}
	return r.bashPPBindPromotedMethod(iv.cell, method, sel, false)
}

func (r *Runner) bashPPBindMethod(cell *bashPPCell, method string, addressable bool) (*bashPPFunc, bool) {
	return r.bashPPBindMethodReceiver(cell, method, addressable, false)
}

// bashPPBindMethodReceiver binds a method to cell. A transported native
// callback receiver is already the value copy on which Go invoked the
// generated mirror method, so copiedReceiver avoids copying that private value
// a second time. Reference fields remain shared exactly as they are in a Go
// value-receiver copy; ordinary call sites keep the usual copy below.
func (r *Runner) bashPPBindMethodReceiver(cell *bashPPCell, method string, addressable, copiedReceiver bool) (*bashPPFunc, bool) {
	fn := r.bashPPMethods[cell.typeName][method]
	if fn == nil {
		r.errf("type %s has no method %s\n", cell.typeName, method)
		r.exit.code = 2
		return nil, false
	}
	ptrRecv := fn.decl.Receiver.Pointer
	if ptrRecv && !cell.pointer && !addressable {
		r.errf("method %s has pointer receiver and requires an addressable %s\n", method, cell.typeName)
		r.exit.code = 2
		return nil, false
	}
	if !ptrRecv && cell.pointer && cell.nilPointer {
		if r.goSourceNilValueReceiver() {
			return nil, false
		}
		r.errf("value method %s called using nil *%s pointer\n", method, cell.typeName)
		r.exit.code = 2
		return nil, false
	}
	bound := *fn
	bound.typeArgs = bashPPMethodTypeArgs(fn, cell)
	if ptrRecv {
		bound.receiver = cell
	} else {
		if copiedReceiver && !cell.pointer {
			bound.receiver = cell
			return &bound, true
		}
		copyCell := *cell.view()
		if cell.pointer && cell.pointerValue != nil {
			value, meta, typ, err := cell.pointerValue.read()
			if err != nil {
				r.errf("%v\n", err)
				r.exit.code = 2
				return nil, false
			}
			copyCell = bashPPCell{declType: typ, typeName: cell.typeName}
			// The pointee is stored the way every other read of a value is.
			// Boxing it here instead once kept Go-source collection storage
			// out of expand.NewObject's validation, but bashPPStoreCellValue
			// now keeps trusted collection payloads verbatim itself, and it
			// alone honours the metadata kinds a receiver can also have:
			// `kind: "scalar"` - what a dependency-decoded named numeric
			// element carries - becomes a scalar cell rather than an object
			// one no arithmetic, conversion or comparison can read.
			bashPPStoreCellValue(&copyCell, value, meta)
		}
		if copyCell.vr.Kind == expand.Object {
			if meta := bashPPCellMeta(&copyCell); bashPPScalarMetaKind(meta) {
				bashPPStoreCellValue(&copyCell, copyCell.vr.Obj, meta)
			} else if bashPPValueMeta(meta) {
				value, copiedMeta := bashPPCopyArrayValue(copyCell.vr.Obj, meta)
				if r.bashPPGoSource {
					// A typed value copy copies fields, not referenced storage.
					copyCell.vr = expand.Variable{Set: true, Kind: expand.Object, Obj: value}
				} else {
					copyCell.vr = expand.NewObject(value)
				}
				copyCell.valueMeta = copiedMeta
				copyCell.object = &bashPPObjectIdentity{collection: copiedMeta}
			}
		}
		copyCell.pointer, copyCell.nilPointer = false, false
		bound.receiver = &copyCell
	}
	return &bound, true
}

func bashPPMethodTypeArgs(fn *bashPPFunc, cell *bashPPCell) map[string]syntax.BashPPTypeExpr {
	if fn == nil || fn.decl == nil || fn.decl.Receiver == nil || len(fn.decl.Receiver.TypeParams) == 0 || cell == nil {
		return nil
	}
	return bashPPMethodTypeBindings(fn, cell.declType)
}

func bashPPMethodTypeBindings(fn *bashPPFunc, typ syntax.BashPPTypeExpr) map[string]syntax.BashPPTypeExpr {
	if fn == nil || fn.decl == nil || fn.decl.Receiver == nil || len(fn.decl.Receiver.TypeParams) == 0 {
		return nil
	}
	if pointer, ok := typ.(*syntax.BashPPPointerType); ok {
		typ = pointer.Element
	}
	named, ok := typ.(*syntax.BashPPNamedType)
	if !ok || len(named.TypeArgs) != len(fn.decl.Receiver.TypeParams) {
		return nil
	}
	bindings := make(map[string]syntax.BashPPTypeExpr, len(named.TypeArgs))
	for i, param := range fn.decl.Receiver.TypeParams {
		bindings[param.Value] = named.TypeArgs[i].ArgType
	}
	return bindings
}

// bashPPCallArgValues evaluates each call argument to a single string. Values
// follow the dialect's existing convention that a bare word is its own literal
// and an expansion is expanded, exactly as a `:=` right-hand side does, so
// `f($x)` passes the value of x while `f(x)` passes the string "x".
//
// A trailing `...` spreads its argument instead of passing it: `f(xs...)`
// hands the elements of xs to a variadic parameter, one argument each.
func (r *Runner) bashPPCallArgValues(c *syntax.BashPPCall) []string {
	values, _ := r.bashPPCallArgValuesWithCells(c)
	return values
}

func (r *Runner) bashPPCallArgValuesWithCells(c *syntax.BashPPCall) ([]string, []*bashPPCell) {
	args := make([]string, 0, len(c.Args))
	cells := make([]*bashPPCell, 0, len(c.Args))
	for i, word := range c.Args {
		if c.Ellipsis.IsValid() && i == len(c.Args)-1 {
			values := r.bashPPSpreadValues(word)
			args = append(args, values...)
			cells = append(cells, make([]*bashPPCell, len(values))...)
			break
		}
		var argExpr syntax.BashPPExpr
		if i < len(c.ArgExprs) {
			argExpr = c.ArgExprs[i]
		}
		var copied *bashPPCell
		cell := r.bashPPCellForArg(word, argExpr)
		if cell == nil {
			cell = r.bashPPGoSourceArgCell(word, argExpr)
		}
		if cell != nil {
			copied = bashPPCopyAssignmentCell(cell)
			// Direct channel authority is restored only from the separately checked
			// owner provenance. A value copy cannot grant an unverified capability.
			copied.channel = nil
			copied.channelOwner = nil
		}
		value := r.bashPPExprValue(word)
		if r.bashPPGoSource && copied != nil && copied.vr.Kind == expand.String {
			value = copied.vr.Str
		}
		args = append(args, value)
		cells = append(cells, copied)
	}
	return args, cells
}

// bashPPSpreadValues expands the `xs...` argument into the values it passes.
//
// The spread reads the NAMED variable rather than the word's expansion because
// that is the only spelling that can carry more than one value: `$xs` has
// already been joined into a single string by the time an expansion is done,
// so spreading it would pass one argument no matter how many elements it held.
// An indexed variable — which is what a variadic parameter binds — spreads
// element by element; a scalar spreads as itself; an unset name spreads as
// nothing, so forwarding an empty variadic parameter passes zero arguments.
func (r *Runner) bashPPSpreadValues(w *syntax.Word) []string {
	if len(w.Parts) == 1 {
		if lit, ok := w.Parts[0].(*syntax.Lit); ok && syntax.BashPPValidIdent(lit.Value) {
			vr := r.lookupVar(lit.Value)
			switch {
			case !vr.IsSet():
				return nil
			case vr.Kind == expand.Indexed:
				return append([]string(nil), vr.List...)
			case vr.Kind == expand.String:
				return []string{vr.Str}
			}
		}
	}
	return []string{r.bashPPExprValue(w)}
}

// bashPPCallValues evaluates a call's arguments for fn, refusing a spread the
// callee cannot accept. Go rejects `f(xs...)` when f is not variadic, and so
// does this: silently passing the elements would make the two spellings mean
// the same thing and hide the mistake.
func (r *Runner) bashPPCallValues(c *syntax.BashPPCall, fn *bashPPFunc) (result []string, success bool) {
	defer func() {
		if success && !r.goSourceFinalizeReceiver(fn) {
			result = nil
			success = false
		}
	}()
	if fn.foreign != nil && fn.foreign.direct {
		fn.foreign.call = c
		args, cells := r.bashPPCallArgValuesWithCells(c)
		seen := map[string]bool{}
		fn.foreign.argNames = fn.foreign.argNames[:0]
		for _, name := range c.ArgNames {
			if seen[name.Value] {
				r.errf("BASHPP-EKWARG-DUPLICATE: argument %q is supplied more than once\n", name.Value)
				r.exit = exitStatus{code: 2}
				return nil, false
			}
			seen[name.Value] = true
			fn.foreign.argNames = append(fn.foreign.argNames, name.Value)
		}
		fn.foreign.argCells = cells
		return args, true
	}
	if c.Ellipsis.IsValid() && !bashppVariadic(fn.params()) {
		r.errf("cannot use ... in call to non-variadic %s\n", fn.name())
		r.exit = exitStatus{code: 2}
		return nil, false
	}
	if r.bashPPGoSource && len(c.ArgExprs) == len(c.Args) {
		args, ok, err := r.goSourceCallArguments(c, fn)
		if err != nil {
			if !errors.Is(err, errBashPPScalarInterrupted) {
				r.errf("%s%v\n", r.bashErrPrefix(c.Pos()), err)
				r.exit = exitStatus{code: 2}
			}
			// A panic raised while an argument was evaluated is unwinding
			// the frame itself; marking the frame failed as well would make
			// it exit 2 after a deferred call has recovered the panic.
			if !r.bashPPPanicking() {
				r.bashPPShortFailureSeq++
			}
			return nil, false
		}
		return args, ok
	}
	r.bashPPCallCells = nil
	r.bashPPCallChannels = nil
	if required := bashppRequiredAfterDefault(fn.params()); required != "" {
		r.errf("BASHPP-EDEFAULT-ORDER: required parameter %q follows a default parameter\n", required)
		r.exit = exitStatus{code: 2}
		return nil, false
	}
	if c.Ellipsis.IsValid() && !bashppVariadic(fn.params()) {
		r.errf("cannot use ... in call to non-variadic %s\n", fn.name())
		r.exit = exitStatus{code: 2}
		return nil, false
	}
	// Capture typed argument provenance before evaluating any argument. An
	// argument expansion may itself invoke a Bash++ function, whose ephemeral
	// call metadata must not overwrite the authority belonging to this call.
	channels := make([]*bashPPChannel, len(c.Args))
	interfaces := make([]*bashPPInterfaceValue, len(c.Args))
	for i, word := range c.Args {
		channel, owner := r.bashPPDirectChannel(word)
		if owner == r.bashPPConcurrent {
			channels[i] = channel
		}
		if cell := r.bashPPCellForWord(word); cell != nil {
			interfaces[i] = cell.interfaceValue
		}
	}
	args, cells := r.bashPPCallArgValuesWithCells(c)
	r.bashPPCallChannels = nil
	r.bashPPCallInterfaces = nil
	names := c.ArgNames
	positional := len(args) - len(names)
	if fn.skipArgs > 0 {
		args = args[fn.skipArgs:]
		channels = channels[fn.skipArgs:]
		interfaces = interfaces[fn.skipArgs:]
		cells = cells[fn.skipArgs:]
		positional -= fn.skipArgs
	}
	if fn.foreign != nil {
		// A typed foreign export reads Object arguments — a Rust handle —
		// from the cells, as the direct Python form already does.
		fn.foreign.argCells = cells
	}
	if len(names) > 0 || bashppHasDefaults(fn.params()) {
		r.bashPPCallInterfaces = interfaces
		return r.bashPPBindCall(fn, args, channels, cells, names, positional)
	}
	r.bashPPCallChannels = channels
	r.bashPPCallInterfaces = interfaces
	r.bashPPCallCells = cells
	return args, true
}

// bashPPBindCall applies the Bash# positional-then-named/default binding
// contract. It is deliberately entered only when a call uses a name or the
// signature has a default, so the established P3 arity diagnostics remain
// byte-for-byte unchanged for ordinary Go-form calls.
func (r *Runner) bashPPBindCall(fn *bashPPFunc, supplied []string, suppliedChannels []*bashPPChannel, suppliedCells []*bashPPCell, names []*syntax.Lit, positional int) ([]string, bool) {
	params := bashppParams(fn.params())
	fail := func(format string, args ...any) ([]string, bool) {
		r.bashPPCallChannels = nil
		r.bashPPCallInterfaces = nil
		r.bashPPCallCells = nil
		r.errf(format, args...)
		r.exit = exitStatus{code: 2}
		return nil, false
	}
	if positional > len(params) {
		return fail("BASHPP-EARG-COUNT: %s accepts at most %d arguments; got %d\n",
			fn.name(), len(params), len(supplied))
	}
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if seen[name.Value] {
			return fail("BASHPP-EKWARG-DUPLICATE: argument %q is supplied more than once\n", name.Value)
		}
		seen[name.Value] = true
	}
	byName := make(map[string]int, len(params))
	for i, param := range params {
		if param.name != "" {
			byName[param.name] = i
		}
	}
	for _, name := range names {
		if _, ok := byName[name.Value]; !ok {
			return fail("BASHPP-EKWARG-UNKNOWN: %s has no parameter named %q\n", fn.name(), name.Value)
		}
	}
	values := make([]string, len(params))
	channels := make([]*bashPPChannel, len(params))
	interfaces := make([]*bashPPInterfaceValue, len(params))
	cells := make([]*bashPPCell, len(params))
	bound := make([]bool, len(params))
	for i := 0; i < positional; i++ {
		values[i], bound[i] = supplied[i], true
		if i < len(suppliedCells) {
			cells[i] = suppliedCells[i]
		}
		if i < len(suppliedChannels) {
			channels[i] = suppliedChannels[i]
		}
		if i < len(r.bashPPCallInterfaces) {
			interfaces[i] = r.bashPPCallInterfaces[i]
		}
	}
	for i, name := range names {
		index := byName[name.Value]
		if bound[index] {
			return fail("BASHPP-EARG-DUPLICATE-BINDING: parameter %q is supplied positionally and by name\n", name.Value)
		}
		values[index], bound[index] = supplied[positional+i], true
		if source := positional + i; source < len(suppliedCells) {
			cells[index] = suppliedCells[source]
		}
		if source := positional + i; source < len(suppliedChannels) {
			channels[index] = suppliedChannels[source]
		}
		if source := positional + i; source < len(r.bashPPCallInterfaces) {
			interfaces[index] = r.bashPPCallInterfaces[source]
		}
	}
	for i, param := range params {
		if bound[i] {
			continue
		}
		if param.defaultValue != nil {
			if cell := r.bashPPCellForWord(param.defaultValue); cell != nil {
				cells[i] = bashPPCopyAssignmentCell(cell)
			}
			values[i], bound[i] = r.bashPPExprValue(param.defaultValue), true
			if channel, owner := r.bashPPDirectChannel(param.defaultValue); owner == r.bashPPConcurrent {
				channels[i] = channel
			}
			continue
		}
		return fail("BASHPP-EARG-MISSING: %s requires argument %q\n", fn.name(), param.name)
	}
	r.bashPPCallChannels = channels
	r.bashPPCallInterfaces = interfaces
	r.bashPPCallCells = cells
	return values, true
}

// bashPPExprValue evaluates the small expression vocabulary admitted by the
// P3-A call grammar. A bare identifier is an expression, not a literal word;
// resolve it through the existing lexical/shell environment when it names a
// live binding. Other bare words remain string literals, preserving the
// convenient `f(hello)` spelling for an unquoted string argument.
func (r *Runner) bashPPExprValue(w *syntax.Word) string {
	if len(w.Parts) == 1 {
		if lit, ok := w.Parts[0].(*syntax.Lit); ok && syntax.BashPPValidIdent(lit.Value) {
			if vr := r.lookupVar(lit.Value); vr.IsSet() {
				return vr.String()
			}
		}
	}
	return r.literal(w)
}

// bashPPRewriteAssign turns a bare identifier RHS into the equivalent short
// parameter expansion for the duration of assignment evaluation. Shell
// syntax has no bare-expression form, while Go-form function bodies do:
// `result = input` must copy input's value rather than the literal text.
func (r *Runner) bashPPRewriteAssign(as *syntax.Assign) *syntax.Assign {
	if r.bashPPFuncActive == 0 || as == nil || as.Value == nil || len(as.Value.Parts) != 1 {
		return as
	}
	lit, ok := as.Value.Parts[0].(*syntax.Lit)
	if !ok || !syntax.BashPPValidIdent(lit.Value) || !r.lookupVar(lit.Value).IsSet() {
		return as
	}
	cp := *as
	cp.Value = &syntax.Word{Parts: []syntax.WordPart{&syntax.ParamExp{
		Dollar: lit.Pos(), Short: true, Param: lit,
	}}}
	return &cp
}

// bashPPRewriteCommandArgs gives bare identifiers their Go-form expression
// meaning inside a typed function body. The command name remains a shell word;
// subsequent words which name live lexical bindings become short parameter
// expansions for this invocation only.
func (r *Runner) bashPPRewriteCommandArgs(args []*syntax.Word) []*syntax.Word {
	if len(args) < 2 {
		return args
	}
	out := append([]*syntax.Word(nil), args[:1]...)
	for i := 1; i < len(args); {
		combined := &syntax.Word{Parts: append([]syntax.WordPart(nil), args[i].Parts...)}
		bestValue, bestEnd := "", -1
		for j := i; j < len(args); j++ {
			if j > i {
				if args[j-1].End() != args[j].Pos() {
					break
				}
				combined.Parts = append(combined.Parts, args[j].Parts...)
			}
			if value, ok := r.bashPPResolveWord(combined); ok {
				bestValue, bestEnd = value, j+1
			}
		}
		if bestEnd >= 0 {
			out = append(out, &syntax.Word{Parts: []syntax.WordPart{&syntax.SglQuoted{
				Left: args[i].Pos(), Right: args[bestEnd-1].End(), Value: bestValue,
			}}})
			i = bestEnd
			continue
		}
		word := args[i]
		if r.bashPPFuncActive == 0 {
			out = append(out, word)
			i++
			continue
		}
		if len(word.Parts) != 1 {
			out = append(out, word)
			i++
			continue
		}
		lit, ok := word.Parts[0].(*syntax.Lit)
		if !ok || !syntax.BashPPValidIdent(lit.Value) || !r.lookupVar(lit.Value).IsSet() {
			out = append(out, word)
			i++
			continue
		}
		out = append(out, &syntax.Word{Parts: []syntax.WordPart{&syntax.ParamExp{
			Dollar: lit.Pos(), Short: true, Param: lit,
		}}})
		i++
	}
	return out
}

// bashPPInvoke calls a typed function with already-evaluated arguments and
// returns its result values. The exit status is set on r: an explicit `return`
// of values succeeds with status 0, while a result-less function keeps the
// body's last status (or the code named by a bash-style `return n`).
func (r *Runner) bashPPInvoke(ctx context.Context, fn *bashPPFunc, args []string) []string {
	r.bashPPResultCells = nil
	callChannels := r.bashPPCallChannels
	callInterfaces := r.bashPPCallInterfaces
	callCells := r.bashPPCallCells
	callSpread := r.bashPPCallSpread
	r.bashPPCallCells = nil
	r.bashPPCallChannels = nil
	r.bashPPCallInterfaces = nil
	r.bashPPCallSpread = false
	if fn.decoratorNext {
		return r.bashPPDecoratorNext(ctx, fn)
	}
	if fn.foreign != nil {
		return r.bashPPInvokeForeign(ctx, fn.foreign, args)
	}
	if fn.native != nil {
		return r.goSourceInvokeNative(ctx, fn, args, callCells)
	}
	if fn.runtimeError != nil {
		return r.bashPPInvokeRuntimeErrorMethod(fn)
	}
	if fn.rangeYield != nil {
		return r.goSourceInvokeRangeYield(ctx, fn, args, callCells, callInterfaces)
	}
	if fn.collectYield != nil {
		return r.goSourceInvokeCollectYield(fn, args, callCells)
	}
	if fn.decl != nil && fn.decl.Agentic != nil && !r.bashPPAgentic {
		r.bashPPAgenticCallError(r.curStmtPos, fn.name())
		return nil
	}
	params := bashppParams(fn.params())
	// A function named as an argument becomes a function value before the
	// type check looks at it, so a parameter declared `func() int` accepts
	// `f` the same way it accepts a closure; see [Runner.bashPPBindFuncValueArgs].
	args, bound := r.bashPPBindFuncValueArgs(params, args)
	if !bound {
		return nil
	}
	var err error
	args, callCells, err = r.goSourceContextualFloatCallArgs(params, args, callCells, callSpread)
	if err != nil {
		r.exit.fatal(err)
		return nil
	}
	if !r.bashPPCheckArgs(fn, params, args, callSpread) {
		return nil
	}
	if !r.bashPPCheckChannelArgs(fn, params, callChannels, callCells) {
		return nil
	}
	// The scalar plan is selected only after the ordinary argument and channel
	// gates have run. It either completes the call without touching runner
	// frame state, or declines before observable work and lets the general
	// evaluator below retain all semantics and diagnostics.
	if results, handled := r.bashPPTryScalarIntInvoke(ctx, fn, args, callCells); handled {
		return results
	}
	if limit := r.bashPPFuncNest(); limit > 0 && len(r.callStack) >= limit {
		r.errf("%s: maximum function nesting level exceeded (%d)\n", fn.name(), limit)
		r.exit.code = 1
		return nil
	}
	// A frame called BY a deferred call of a running panic must leave the
	// panic running when it returns, whatever its own defers did; see
	// goSourceNestedDeferRunning.
	enteredRunning, enteredChain := r.bashPPPanic.running, len(r.bashPPPanic.chain)

	// Save the caller's execution context and restore it with a defer, so
	// that EVERY exit path — a return, a panic unwinding through this frame,
	// a hard exit, a host-level failure — leaves the caller's params, scope,
	// environment and call stack exactly as they were. See [bashPPFrame].
	frame := r.bashPPEnterFrame(fn, args)
	defer frame.leave()
	if r.goSourceTesting != nil {
		defer r.bashPPTestingUnwind(ctx, frame.deferMark)
	}
	shortFailureMark := r.bashPPShortFailureSeq

	if !r.bashPPBindParams(params, args, callCells, callChannels, callInterfaces, callSpread) {
		return nil
	}
	resultNames := bashppResultNames(fn.bodyResults())
	if r.bashPPGoSource {
		if !r.goSourceDeclareResults(ctx, fn.bodyResults()) {
			return nil
		}
	} else {
		for _, name := range resultNames {
			if name != "" {
				_ = r.bashPPScope.declare(name, expand.Variable{Set: true, Kind: expand.String, Str: ""}, false)
			}
		}
	}

	// A decorated declaration runs its body through the decorator chain.
	// The chain sits here on purpose: after the agentic, argument and
	// FUNCNEST gates above, inside the frame, so a decorator cannot smuggle
	// a marked action out of scope and the frame's defers still bracket it.
	decorated := false
	var decoratedResults []string
	if fn.decl != nil && (len(fn.decl.Decorators) > 0 || len(fn.advised) > 0) {
		decorated = true
		decoratedResults, _ = r.bashPPInvokeDecorated(ctx, fn, args, callCells, callChannels, resultNames, bashPPDecoratorRungs(fn.decl.Decorators, fn.advised))
	} else if body := fn.body(); body != nil {
		r.bashPPRunBody(ctx, body.Stmts)
	}

	// Results are settled BEFORE the deferred calls run, as Go sets a
	// function's results before running its defers. That ordering is what
	// lets a deferred call change a NAMED result — including the recovering
	// defer, whose whole job is to replace the value an abandoned frame would
	// otherwise have failed to produce.
	shortDeclFailed := r.bashPPShortFailureSeq != shortFailureMark
	var results []string
	if decorated {
		results = decoratedResults
	} else if !shortDeclFailed {
		results = r.bashPPSettleResults(fn, resultNames)
	}

	// Deferred calls run as the frame unwinds — on a normal return and on a
	// panic alike, which is the point of them — but not through a hard shell
	// `exit`, which is terminating everything.
	if !r.exit.exiting {
		r.bashPPRunDefers(ctx, frame.deferMark)
	} else if !r.bashPPTestingCancelUnwind(ctx, frame.deferMark) {
		r.bashPPDeferStack = r.bashPPDeferStack[:frame.deferMark]
	}
	if shortDeclFailed {
		// A fatal diagnostic recorded inside the body — a dependency call
		// the bridge refused, say — is the outcome; a `return f()` whose f
		// failed that way counts as a failed producer too, and must not
		// replace the diagnostic with a bare status.
		if !r.exit.fatalExit && !r.exit.exiting {
			r.exit = exitStatus{code: 2}
		}
		return nil
	}

	// An explicit `exit` reached while a panic was unwinding terminates the
	// script with the status it named, and the panic is neither reported nor
	// propagated: the shell is leaving on purpose, not crashing.
	if r.bashPPPanicSettledByExit() {
		return nil
	}
	// A frame called by a deferred call of a running panic returns normally
	// — the panic is running, not halting — so only a halting panic marks
	// this frame abandoned.
	r.bashPPPanic.running = r.goSourceNestedDeferRunning(enteredRunning, enteredChain)
	if r.bashPPPanicHalts() {
		// The frame was abandoned, not returned from: it has no results, and
		// the panic continues into the caller unless this was the last frame
		// that could have recovered it.
		if r.bashPPFuncActive == 1 {
			r.bashPPPanicTerminate()
		} else {
			r.bashPPUnwind()
		}
		return nil
	}
	results, settled := r.bashPPSettleResultCells(fn, results, resultNames, decorated)
	if !settled {
		return nil
	}
	// A status recover used to REPORT its answer — "nothing to recover",
	// which is what the bare `recover()` of a guard like `defer rec()` says
	// on a frame that did not panic — is that call's, not this frame's. A
	// Go function has no status of its own, so the answer is consumed at
	// the func boundary too: left in place it made a task whose last frame
	// ended on such a guard a failed task, and a program whose main did so
	// exit 1. The stamp is what tells this status from a real failure that
	// merely inherited the exemption.
	if r.bashPPGoSource && r.exit.recoverSeq != 0 && r.exit.errexitExempt && !r.exit.exiting && !r.exit.fatalExit {
		r.exit.code, r.exit.err, r.exit.recoverSeq, r.exit.errexitExempt = 0, nil, 0, false
	}
	// A Go-form return is consumed at the func boundary, exactly as a shell
	// function's `return` is in [Runner.call]; it must not unwind the caller.
	r.exit.returning = false
	return results
}

// bashPPBindParams binds a call's arguments to the callee's parameters in the
// frame [Runner.bashPPInvoke] has just entered, and reports whether the call
// may proceed. It is a function of its own, as [Runner.bashPPSettleResultCells]
// is, so that its locals are not held on the host stack for the whole of every
// interpreted call level.
func (r *Runner) bashPPBindParams(params []bashPPParam, args []string, callCells []*bashPPCell, callChannels []*bashPPChannel, callInterfaces []*bashPPInterfaceValue, callSpread bool) bool {
	// Parameters and named results are typed bindings; a shell assignment in
	// the body writes through to them, which is what lets a named result be
	// set with `n=5` and read back by a bare return.
	//
	// The variadic parameter binds the REMAINING arguments as an indexed
	// variable, so the body reads them with the array spellings the shell
	// already has — `${rest[@]}`, `${#rest[@]}` — and can forward them with
	// `rest...`. Zero remaining arguments still bind the name, to an empty
	// list: a variadic parameter is never unset, exactly as a nil slice in Go
	// is still a slice.
	for i, param := range params {
		if param.variadic {
			// An original Go program's variadic parameter IS a slice: it is
			// printed as one, measured with len, indexed, resliced and passed
			// on. The indexed shell binding below carries none of that, so a
			// Go-source frame binds a real slice cell instead.
			if r.bashPPGoSource && param.name != "" {
				if !r.goSourceBindVariadic(param, args[i:], callCells[min(i, len(callCells)):], callSpread) {
					return false
				}
				break
			}
			if param.name != "" {
				rest := append([]string(nil), args[i:]...)
				_ = r.bashPPScope.declare(param.name,
					expand.Variable{Set: true, Kind: expand.Indexed, List: rest}, false)
				// The indexed binding above is what lets the body keep using
				// `${rest[@]}`; this collection meta rides alongside it so a
				// two-variable `for i, v := range rest` sees each element's value
				// and declared type instead of falling through to the scalar
				// range path, which has no notion of a list at all and would
				// treat len(rest) as an integer to count up to.
				r.bashPPScope.lookup(param.name).valueMeta = &bashPPCollectionMeta{
					kind:     "slice",
					typ:      &syntax.BashPPCollectionType{Kind: "slice", Element: param.typ},
					sequence: make([]*bashPPCollectionMeta, len(rest)),
				}
			}
			break
		}
		if param.name == "" {
			continue
		}
		if i < len(callCells) && callCells[i] != nil {
			copy, err := r.goSourceExpectedCell(callCells[i], param.typ)
			if err != nil {
				r.exit.fatal(err)
				return false
			}
			// The parameter owns a private copy of the argument. A cell
			// the expected-type conversion already made is that copy.
			if copy == callCells[i] {
				copy = bashPPCopyAssignmentCell(copy)
			}
			copy.channel = nil
			copy.channelOwner = nil
			copy.constant = false
			copy.vr.ReadOnly = false
			copy.vr.Exported = false
			if err := r.bashPPBindInterfaceParam(copy, param.typ); err != nil {
				r.errf("%v\n", err)
				r.exit = exitStatus{code: 2}
				return false
			}
			if param.typ != nil {
				copy.declType = param.typ
			}
			r.bashPPScope.put(param.name, copy)
		} else {
			_ = r.bashPPScope.declare(param.name,
				expand.Variable{Set: true, Kind: expand.String, Str: args[i]}, false)
		}
		if i < len(callChannels) && callChannels[i] != nil {
			cell := r.bashPPScope.lookup(param.name)
			cell.channel, cell.channelOwner = callChannels[i], r.bashPPConcurrent
		}
		if i < len(callInterfaces) && callInterfaces[i] != nil && !(r.bashPPGoSource && i < len(callCells) && callCells[i] != nil) {
			cell := r.bashPPScope.lookup(param.name)
			cell.interfaceValue = callInterfaces[i]
		}
		if base := strings.TrimPrefix(param.declared, "*"); base != "" {
			if _, ok := r.bashPPTypes[base]; ok {
				cell := r.bashPPScope.lookup(param.name)
				cell.typeName = base
				cell.pointer = r.bashPPDeclaredPointer(param.declared) || cell.pointer && r.goSourceUnsafePointerType(param.typ)
				cell.nilPointer = cell.pointer && args[i] == ""
				if r.bashPPGoSource && cell.pointer {
					cell.nilPointer = cell.view().pointerValue == nil
				}
			}
		}
	}
	return true
}

// bashPPSettleResultCells builds the result cells a finished frame hands to
// its caller, and reports whether the results are valid.
func (r *Runner) bashPPSettleResultCells(fn *bashPPFunc, results, resultNames []string, decorated bool) ([]string, bool) {
	// A named result may have been reassigned by a deferred call, so it is
	// read here rather than trusted from before the defers ran.
	results = r.bashPPFinalResults(results, resultNames)
	resultTypes := bashppResultTypeExprs(fn.results())
	decoratorCells := r.bashPPResultCells
	r.bashPPResultCells = make([]*bashPPCell, len(results))
	for i := range results {
		var source *bashPPCell
		blankResult := i < len(resultNames) && resultNames[i] == "_"
		if i < len(resultNames) && resultNames[i] != "" && !blankResult {
			source = r.bashPPScope.lookup(resultNames[i])
		} else if decorated && i < len(decoratorCells) {
			source = decoratorCells[i]
		} else if i < len(r.bashPPReturn.cells) {
			source = r.bashPPReturn.cells[i]
		}
		// A blank or unassigned result parameter has a result slot but no
		// lexical binding. On a naked return or when recovering from a panic,
		// instantiate that slot directly from its declared type; looking up
		// "_" or "" can only find unrelated shell state (or nothing), while
		// rebuilding the slot from results[i] loses non-string zero values.
		// An explicit return reaches the branch above and keeps its value cell.
		if source == nil && ((r.bashPPGoSource && !r.bashPPReturn.active) || blankResult) && i < len(resultTypes) && resultTypes[i] != nil {
			resultType := r.bashPPBindTypeExpr(resultTypes[i])
			zero, meta := r.bashPPZeroValue(resultType)
			source = &bashPPCell{declType: resultType, typeName: bashPPNamedTypeBase(resultType)}
			bashPPStoreCellValue(source, zero, meta)
			results[i] = source.vr.String()
		}
		if i < len(resultTypes) && !r.bashPPCheckChannelResult(fn, resultTypes[i], source) {
			r.bashPPResultCells = nil
			return nil, false
		}
		if source != nil {
			r.bashPPResultCells[i] = bashPPCopyAssignmentCell(source)
		} else {
			r.bashPPResultCells[i] = &bashPPCell{vr: expand.Variable{Set: true, Kind: expand.String, Str: results[i]}}
		}
		if i < len(resultTypes) {
			var err error
			r.bashPPResultCells[i], err = r.goSourceExpectedCell(r.bashPPResultCells[i], resultTypes[i])
			if err != nil {
				r.exit.fatal(err)
				r.bashPPResultCells = nil
				return nil, false
			}
			r.bashPPResultCells[i].declType = resultTypes[i]
			r.bashPPResultCells[i].typeName = bashPPNamedTypeBase(resultTypes[i])
		}
	}
	return results, true
}

// bashPPFrame is one Go-form invocation's saved caller state.
//
// It exists so that entering and leaving a frame are ONE decision each rather
// than a dozen assignments repeated per exit path. Every field here is
// something a body can change and a caller must not observe changed; leaving
// restores all of them, and because it is called through a defer it restores
// them even on the paths that do not reach the end of the invoker.
type bashPPFrame struct {
	agentic    bool
	r          *Runner
	params     []string
	inFunc     bool
	writeEnv   expand.WriteEnviron
	own        *overlayEnviron
	scope      *bashPPScope
	callDepth  int
	deferMark  int
	rangeDefer *[]bashPPDeferred
	ret        bashPPReturnState
	deferDepth int
	// typeArgs is the caller's type parameter bindings, restored on leave so
	// a generic frame's `T` cannot outlive the call that bound it.
	typeArgs map[string]syntax.BashPPTypeExpr
	// branch, branchDepth and gotoLabel are the caller's pending Go-form
	// break/continue/goto, saved so the callee starts clean. A labeled
	// break/continue can never target a loop outside its own function — go/
	// types rejects that statically — so a callee's OWN loops must never see,
	// let alone consume, a level of a label escape that belongs to the
	// caller's labeled statement. Without this a callee that ranges over its
	// own unrelated slice (for instance a range-over-func iterator like
	// rangefunc_test.go's BadOfSliceIndex, called from inside the loop body
	// the caller's escaping branch is unwinding through) steals one level of
	// the caller's escape budget merely by also being "a loop" as far as
	// [Runner.bashPPRangeControl] can tell, which then targets the wrong
	// ancestor. See [Runner.bashPPEnterFrame].
	branch      bashPPBranchKind
	branchDepth int
	gotoLabel   string
}

// bashPPEnterFrame pushes the frame fn's body runs in: its own shell function
// scope (so `local` and non-`local` assignments behave as in a shell function),
// its own typed scope chained to the captured definition scope (so closures
// resolve where they were written), its own positional parameters, and its own
// mark on the deferred-call stack.
func (r *Runner) bashPPEnterFrame(fn *bashPPFunc, args []string) *bashPPFrame {
	frame := &bashPPFrame{
		agentic:     r.bashPPAgentic,
		r:           r,
		params:      r.Params,
		inFunc:      r.inFunc,
		writeEnv:    r.writeEnv,
		scope:       r.bashPPScope,
		callDepth:   len(r.callStack),
		deferMark:   len(r.bashPPDeferStack),
		rangeDefer:  r.bashPPRangeDefer,
		ret:         r.bashPPReturn,
		deferDepth:  r.bashPPDeferDepth,
		typeArgs:    r.bashPPTypeParamArgs,
		branch:      r.bashPPBranch,
		branchDepth: r.bashPPBranchDepth,
		gotoLabel:   r.bashPPGotoLabel,
	}
	// A call made from a range-over-function body starts a frame of its own, so
	// its defers belong to it and not to the range's enclosing function. Clear
	// the sink for the callee; leave restores it for the body that resumes.
	r.bashPPRangeDefer = nil
	// A function call is a break/continue/goto boundary: the callee's own
	// loops must start clean, not see a caller's still-unresolved labeled
	// branch. See the field comments on [bashPPFrame].
	r.bashPPBranch, r.bashPPBranchDepth, r.bashPPGotoLabel = bashPPBranchNone, 0, ""
	// The callee's own type arguments REPLACE the caller's rather than
	// extending them. An ordinary function called from inside a generic body
	// has none, and must not inherit a `T` it never declared.
	r.bashPPTypeParamArgs = fn.typeArgs
	r.bashPPAgentic = fn.decl != nil && fn.decl.Agentic != nil
	r.Params = args
	r.inFunc = true
	frame.own = newFuncScopeEnviron(r.writeEnv, false)
	r.writeEnv = frame.own
	r.bashPPScope = newBashPPScope(fn.scope)
	if fn.decl != nil && fn.decl.Receiver != nil && fn.decl.Receiver.Name != nil && fn.decl.Receiver.Name.Value != "_" && fn.receiver != nil {
		recv := fn.decl.Receiver
		if recv.Pointer {
			r.bashPPScope.put(recv.Name.Value, fn.receiver)
		} else {
			copyCell := *fn.receiver.view()
			r.bashPPScope.put(recv.Name.Value, &copyCell)
		}
	}
	r.goSourceEnterInitializerFrame()
	r.callStack = append(r.callStack, callFrame{funcName: fn.name(), callPos: r.curStmtPos, bashPPFn: fn, seq: r.goSourceNextFrameSeq(), frameScope: r.bashPPScope})
	r.bashPPReturn = bashPPReturnState{}
	r.bashPPFuncActive++
	return frame
}

// bashPPShadowedType is one function-local type declaration that shadowed a
// registry entry: the depth of the frame that declared it, its name, and the
// entry it displaced (nil when the name was free).
type bashPPShadowedType struct {
	depth int
	name  string
	prev  *bashPPType
}

// bashPPShadowLocalType lets a type declared inside a Go-form function body
// take a name the registry already holds. Go scopes a local type to its
// block, so two functions may each declare `type s struct{…}`, and a body
// re-executed declares its type again; the runtime's registry is flat, so
// the declaration shadows the entry for the frame's lifetime and
// [bashPPFrame.leave] puts the previous one back. It reports whether the
// declaration is such a shadowing one.
func (r *Runner) bashPPShadowLocalType(name string) bool {
	if !r.bashPPGoSource || r.bashPPFuncActive == 0 {
		return false
	}
	var prev *bashPPType
	if existing, ok := r.bashPPTypes[name]; ok {
		prev = &existing
	}
	// The declaration this answers for writes the flat registry, and
	// [bashPPFrame.leave] writes it again to restore prev. Take ownership now
	// so a parallel callback frame declaring its own local type cannot see
	// either write; see [bashPPSharedTables].
	r.bashPPUnshareTypes()
	r.bashPPShadowedTypes = append(r.bashPPShadowedTypes, bashPPShadowedType{depth: len(r.callStack), name: name, prev: prev})
	return true
}

// leave restores the caller's execution context.
//
// It is deliberately tolerant about depth: it truncates the call and defer
// stacks back to where the frame began rather than popping a fixed count, so a
// frame abandoned mid-unwind cannot leave a deeper stack behind or pop one
// entry too many.
func (f *bashPPFrame) leave() {
	r := f.r
	for len(r.bashPPShadowedTypes) > 0 {
		local := r.bashPPShadowedTypes[len(r.bashPPShadowedTypes)-1]
		if local.depth <= f.callDepth {
			break
		}
		r.bashPPShadowedTypes = r.bashPPShadowedTypes[:len(r.bashPPShadowedTypes)-1]
		r.bashPPUnshareTypes()
		if local.prev != nil {
			r.bashPPTypes[local.name] = *local.prev
		} else {
			delete(r.bashPPTypes, local.name)
		}
		r.bashPPInvalidateSelectionCache()
	}
	r.bashPPAgentic = f.agentic
	f.own.release()
	r.writeEnv = f.writeEnv
	r.bashPPScope = f.scope
	if len(r.callStack) > f.callDepth {
		r.callStack = r.callStack[:f.callDepth]
	}
	if len(r.bashPPDeferStack) > f.deferMark {
		r.bashPPDeferStack = r.bashPPDeferStack[:f.deferMark]
	}
	r.Params = f.params
	r.inFunc = f.inFunc
	r.bashPPReturn = f.ret
	r.bashPPDeferDepth = f.deferDepth
	r.bashPPRangeDefer = f.rangeDefer
	// Restore the caller's own pending branch, discarding anything the callee
	// left behind: a callee's loops are none of the caller's labels' business
	// on the way in (see bashPPEnterFrame) or out. See the field comments on
	// [bashPPFrame].
	r.bashPPBranch, r.bashPPBranchDepth, r.bashPPGotoLabel = f.branch, f.branchDepth, f.gotoLabel
	r.bashPPTypeParamArgs = f.typeArgs
	r.bashPPFuncActive--
}

// bashPPShortDeclCall invokes a typed function for `x := f()` / `a, b := g()`
// and binds its results to the left-hand names positionally, reporting an arity
// mismatch against the function's actual results rather than the call's text.
func (r *Runner) bashPPShortDeclCall(ctx context.Context, d *syntax.BashPPShortDecl, fn *bashPPFunc) {
	args, ok := r.bashPPCallValues(d.Call, fn)
	if !ok {
		return
	}
	shortFailureMark := r.bashPPShortFailureSeq
	effectiveResults := fn.results()
	var results []string
	if fn.foreign != nil && (!fn.foreign.direct || fn.foreign.receiver != nil) && !fn.foreign.export.Signature.Dynamic && len(d.Lhs) == len(fn.foreign.export.Signature.Results)+1 {
		// One binding more than the export's results is the explicit error
		// opt-in; handle methods have one dynamic value plus the optional error.
		results = r.bashPPInvokeForeignErr(ctx, fn.foreign, args)
		effectiveResults = append(append([]*syntax.BashPPField(nil), effectiveResults...), &syntax.BashPPField{FieldType: &syntax.Lit{Value: "error"}})
	} else {
		results = r.bashPPInvoke(ctx, fn, args)
	}
	// A call abandoned by panic or hard termination produced no values. Do not
	// turn that control transfer into a secondary assignment-mismatch error;
	// the caller's frame must get the original unwind unchanged.
	if r.bashPPPanicking() || r.exit.exiting || r.exit.fatalExit || r.exit.err != nil || r.bashPPShortFailureSeq != shortFailureMark {
		return
	}
	if len(d.Lhs) != len(results) {
		r.errf("assignment mismatch: %d variable(s) but %d value(s)\n",
			len(d.Lhs), len(results))
		r.exit = exitStatus{code: 2}
		return
	}
	resultTypes := bashppResultTypes(effectiveResults)
	resultTypeExprs := bashppResultTypeExprs(effectiveResults)
	for i, lhs := range d.Lhs {
		// Go's blank identifier discards the result: `_, err := f()` declares
		// no binding for the first result, so there is nothing to look up and
		// nothing to attach that result's provenance to.
		if lhs.Value == "_" {
			continue
		}
		if !syntax.BashPPValidIdent(lhs.Value) {
			r.errf("invalid variable name: %q\n", lhs.Value)
			r.exit = exitStatus{code: 2}
			return
		}
		r.bashPPDeclareName(lhs.Value, expand.Variable{Set: true, Kind: expand.String, Str: results[i]})
		target := r.bashPPScope.lookup(lhs.Value)
		if target == nil {
			continue
		}
		if i < len(r.bashPPResultCells) && r.bashPPResultCells[i] != nil {
			source := r.bashPPResultCells[i]
			target.channel, target.channelOwner = source.channel, source.channelOwner
			// A structured result is the value itself, not its text: a
			// pointer, an interface and an object payload have to survive the
			// hand-off or the binding names an empty string.
			target.interfaceValue = source.interfaceValue
			target.pointer, target.nilPointer, target.pointerValue = source.pointer, source.nilPointer, source.pointerValue
			if source.vr.Kind == expand.Object || source.pointer || source.interfaceValue != nil {
				target.vr, target.object, target.valueMeta = source.vr, source.object, source.valueMeta
			}
		}
		if i < len(resultTypeExprs) {
			target.declType = resultTypeExprs[i]
		}
		if i < len(resultTypes) {
			declared := resultTypes[i]
			base := strings.TrimPrefix(declared, "*")
			if _, ok := r.bashPPTypes[base]; ok {
				pointer := r.bashPPDeclaredPointer(declared)
				target.typeName = base
				target.pointer = pointer
				// A pointer binding's text is always empty, so the result cell
				// is what distinguishes a live pointer from a nil one.
				target.nilPointer = pointer && target.view().pointerValue == nil && results[i] == ""
			}
		}
	}
}

// bashPPSettleResults reconciles what a function returns with what it declared,
// setting the exit status as a side effect and reporting an arity mismatch.
//
// It runs BEFORE the frame's deferred calls, and for a NAMED result that
// matters twice over: the returned value is written into the result's binding,
// so a deferred call sees what is being returned and can replace it, and
// [Runner.bashPPFinalResults] then reads the binding back after the defers
// have had their say. That is Go's rule — "the deferred functions run after
// the result parameters are set" — expressed in the only two places that can
// observe it.
func (r *Runner) bashPPSettleResults(fn *bashPPFunc, resultNames []string) []string {
	count := bashppResultCount(fn.bodyResults())
	ret := r.bashPPReturn

	// A result-less function keeps shell semantics for `return`: a bare return
	// or falling off the end yields the body's last status, and `return n`
	// yields status n, mirroring the builtin.
	if count == 0 {
		if ret.active && len(ret.values) == 1 {
			if code, ok := bashppExitCode(ret.values[0]); ok {
				r.exit.code = code
			}
		} else if ret.active && len(ret.values) > 1 {
			r.errf("%s: too many return values for a function with no results\n", fn.name())
			r.exit.code = 2
		}
		return nil
	}

	// A return that names values must name exactly as many as declared.
	if ret.active && len(ret.values) > 0 {
		if len(ret.values) != count {
			r.errf("%s: returned %d value(s) but declared %d result(s)\n",
				fn.name(), len(ret.values), count)
			r.exit.code = 2
			return nil
		}
		resultTypes := bashppResultTypeExprs(fn.bodyResults())
		if r.bashPPGoSource {
			for i, cell := range ret.cells {
				if i >= len(resultTypes) || i >= len(ret.values) || cell == nil {
					continue
				}
				typed, err := r.goSourceExpectedCell(cell, resultTypes[i])
				if err != nil {
					r.exit.fatal(err)
					return nil
				}
				ret.cells[i] = typed
				ret.values[i] = typed.vr.String()
			}
		}
		for i, name := range resultNames {
			if name != "" && name != "_" && i < len(ret.values) {
				target := r.bashPPScope.lookup(name)
				if target != nil && i < len(ret.cells) && ret.cells[i] != nil {
					// Attributes belong to the declaration, so they are merged
					// into the result BEFORE it is stored: patching them back
					// afterwards would republish the binding twice.
					view := target.view()
					merged := *bashPPCopyAssignmentCell(ret.cells[i])
					merged.constant = view.constant
					merged.vr.ReadOnly, merged.vr.Exported = view.vr.ReadOnly, view.vr.Exported
					target.publish(&merged)
				} else {
					r.setVarString(name, ret.values[i])
					target = r.bashPPScope.lookup(name)
				}
				if target != nil && i < len(resultTypes) {
					target.declType = resultTypes[i]
					target.typeName = bashPPNamedTypeBase(resultTypes[i])
				}
			}
		}
		r.exit.clear()
		return ret.values
	}

	// A bare return, or the end of the body, yields the current values of the
	// named results (their zero value if never assigned). An unnamed result
	// with no explicit return yields its zero value, an empty string.
	out := make([]string, 0, count)
	for _, name := range resultNames {
		if name == "" || name == "_" {
			out = append(out, "")
			continue
		}
		out = append(out, r.envGet(name))
	}
	// If the declaration mixed styles or listed only unnamed results, pad to
	// the declared arity with zero values so callers always see `count` items.
	for len(out) < count {
		out = append(out, "")
	}
	r.exit.clear()
	return out
}

// bashPPFinalResults is what the caller actually receives, read after the
// frame's deferred calls have run.
//
// Only a NAMED result is re-read. An unnamed result has no binding a deferred
// call could reach, in Go or here, so its settled value is final; re-reading a
// name that does not exist would replace it with an empty string.
func (r *Runner) bashPPFinalResults(settled []string, resultNames []string) []string {
	out := settled
	for i, name := range resultNames {
		if name == "" || name == "_" || i >= len(out) {
			continue
		}
		out[i] = r.envGet(name)
	}
	return out
}

// bashPPReturnStmt evaluates a Go-form return, recording its values and
// unwinding the body through the shell's existing return machinery.
func (r *Runner) bashPPReturnStmt(ctx context.Context, ret *syntax.BashPPReturn) {
	if r.bashPPGoSource && len(ret.ResultExprs) > 0 {
		r.goSourceReturnValues(ret.ResultExprs)
		return
	}
	if r.bashPPBridgeHandles(ret.Call) {
		r.bashPPReturnBridgeCall(ctx, ret)
		return
	}
	if ret.Call != nil {
		if r.bashPPReturnCallValue(ret) {
			return
		}
		r.bashPPReturnCallInvoke(ctx, ret)
		return
	}
	if ret.FuncLit != nil {
		// `return func(…) { … }` — the factory idiom. The closure captures the
		// frame that is about to unwind, which is exactly what makes it useful:
		// the cells stay alive because the capture holds them, not because the
		// frame does.
		_, vr := r.bashPPMakeClosure(ret.FuncLit)
		r.bashPPReturn = bashPPReturnState{active: true, values: []string{vr.Str}}
		r.exit.returning = true
		return
	}
	if ret.Expr != nil {
		r.bashPPReturnScalarExpr(ret.Expr)
		return
	}
	r.bashPPReturnWords(ret)
}

// The helpers below are the arms of [Runner.bashPPReturnStmt]. Each has a host
// frame of its own: `return f(x)` re-enters the evaluator once per interpreted
// call level, and one shared frame would reserve every arm's locals at each.

// bashPPReturnBridgeCall returns the results of a dependency call.
func (r *Runner) bashPPReturnBridgeCall(ctx context.Context, ret *syntax.BashPPReturn) {
	values, err := r.bashPPBridgeCall(ctx, ret.Call)
	if err != nil {
		if !r.bashPPPanicking() {
			r.exit.fatal(err)
		}
		return
	}
	result := bashPPReturnState{active: true}
	for _, value := range values {
		var cell *bashPPCell
		if r.bashPPGoSource {
			// The dependency's result crosses as the value it is —
			// a nil *map[int]bool from reflect keeps its type name —
			// so a declared interface result can box it with that
			// dynamic type and a caller's assertion sees it (bug510).
			cell = r.goSourceNativeValueCell(value)
		} else if scalar, err := value.scalar(); err == nil {
			cell = &bashPPCell{vr: expand.Variable{Set: true, Kind: expand.String, Str: bashPPScalarString(scalar.value)}, scalarKind: scalar.value.Kind(), typeName: value.Type, declType: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: value.Type}}}
		} else {
			copy := value
			cell = &bashPPCell{vr: expand.NewObject(&copy)}
		}
		result.values = append(result.values, cell.vr.String())
		result.cells = append(result.cells, cell)
	}
	r.bashPPReturn = result
	r.exit.returning = true
}

// bashPPReturnCallValue handles a returned call that is not a call of a
// declared callable — a conversion, an allocation, a builtin, recover — and
// reports whether it was one.
func (r *Runner) bashPPReturnCallValue(ret *syntax.BashPPReturn) bool {
	// `return float64(f)` is a conversion, not a call; the Go front end
	// cannot tell the two apart from syntax alone and delivers both as a
	// call, so the conversion is recognized here rather than refused as an
	// undeclared callable.
	if conv, ok := r.bashPPConversionCall(ret.Call); ok {
		r.bashPPReturnScalarExpr(conv)
		return true
	}
	// `return new(T)` likewise arrives as a call; it is the allocation
	// the Go front end spells as a NewExpr everywhere else.
	if alloc, ok := r.goSourceNewCall(ret.Call); ok {
		r.bashPPReturnScalarExpr(alloc)
		return true
	}
	if cell, handled, err := r.goSourceBuiltinResult(ret.Call); handled {
		if err != nil {
			r.bashPPShortFailureSeq++
			return true
		}
		r.bashPPReturn = bashPPReturnState{active: true, values: []string{cell.vr.String()}, cells: []*bashPPCell{bashPPCopyAssignmentCell(cell)}}
		r.exit.returning = true
		return true
	}
	// `return recover()`: the predeclared recover yields the interface
	// value it recovered (or the nil interface), never a declared callable.
	if cell, handled := r.bashPPReturnRecover(ret.Call); handled {
		r.bashPPReturn = bashPPReturnState{active: true, values: []string{cell.vr.String()}, cells: []*bashPPCell{cell}}
		r.exit.returning = true
		return true
	}
	return false
}

// bashPPReturnCallInvoke returns the results of calling a declared callable.
func (r *Runner) bashPPReturnCallInvoke(ctx context.Context, ret *syntax.BashPPReturn) {
	fn, ok := r.bashPPLookupFunc(ret.Call)
	if !ok {
		// A lookup that raised — a method selected on a nil interface —
		// is unwinding the frame; marking it failed as well would make
		// the frame exit 2 after a deferred call has recovered the panic.
		if r.bashPPPanicking() {
			return
		}
		r.bashPPShortFailureSeq++
		if r.exit.code == 0 {
			r.errf("BASHPP-ERETURN-CALL: return requires a declared callable\n")
			r.exit = exitStatus{code: 2}
		}
		return
	}
	args, ok := r.bashPPCallValues(ret.Call, fn)
	if !ok {
		r.bashPPShortFailureSeq++
		return
	}
	failureMark := r.bashPPShortFailureSeq
	values := r.bashPPInvoke(ctx, fn, args)
	if r.exit.code != 0 && !r.bashPPPanicking() {
		r.bashPPShortFailureSeq++
		return
	}
	if r.bashPPPanicking() || r.exit.exiting || r.exit.fatalExit || r.exit.err != nil || r.bashPPShortFailureSeq != failureMark {
		return
	}
	cells := append([]*bashPPCell(nil), r.bashPPResultCells...)
	r.bashPPReturn = bashPPReturnState{active: true, values: values, cells: cells}
	r.exit.returning = true
}

// bashPPReturnWords returns shell-word results.
func (r *Runner) bashPPReturnWords(ret *syntax.BashPPReturn) {
	vals := make([]string, len(ret.Results))
	cells := make([]*bashPPCell, len(ret.Results))
	for i, w := range ret.Results {
		vals[i] = r.bashPPExprValue(w)
		if source := r.bashPPCellForWord(w); source != nil {
			cells[i] = bashPPCopyAssignmentCell(source)
		}
	}
	r.bashPPReturn = bashPPReturnState{active: true, values: vals, cells: cells}
	r.exit.returning = true
}

// bashPPReturnScalarExpr settles a single scalar result, retaining the value's
// type so a defined type reaches the caller as itself.
func (r *Runner) bashPPReturnScalarExpr(expr syntax.BashPPExpr) {
	if r.bashPPReturnValueCell(expr) {
		return
	}
	r.bashPPReturnScalarValue(expr)
}

// bashPPReturnValueCell handles a returned expression whose value is carried
// by a cell rather than computed as a scalar, and reports whether expr was
// one. It is separate from [Runner.bashPPReturnScalarValue] so that neither
// reserves the other's locals on the host stack of a recursive return.
func (r *Runner) bashPPReturnValueCell(expr syntax.BashPPExpr) bool {
	// Only the mirrored method frame's own returned expression may defer an
	// imported composite to the callback worker's declared reflect result type.
	// Locals and nested helper returns inside the callback still materialise in
	// the dependency immediately, preserving their methods and mutations.
	if r.bashPPGoSource && r.bashPPCallbackReturnDepth == r.bashPPFuncActive {
		boundary := expr
		for {
			paren, ok := boundary.(*syntax.BashPPParenExpr)
			if !ok {
				break
			}
			boundary = paren.X
		}
		if lit, ok := boundary.(*syntax.BashPPCompositeLit); ok && r.bashPPNativeType(lit.LitType) {
			value, err := r.bashPPNativeCompositeAtBoundary(lit, false, true)
			if err != nil {
				r.exit.fatal(&goSourceError{prefix: r.bashErrPrefix(expr.Pos()), err: err})
				r.bashPPShortFailureSeq++
				return true
			}
			cell := goSourceNativeValueCell(value)
			r.bashPPReturn = bashPPReturnState{active: true, values: []string{cell.vr.String()}, cells: []*bashPPCell{cell}}
			r.exit.returning = true
			return true
		}
	}
	if cell, handled, err := r.goSourceNilValueCell(expr); handled {
		if err != nil {
			r.exit.fatal(err)
			return true
		}
		r.bashPPReturn = bashPPReturnState{active: true, values: []string{cell.vr.String()}, cells: []*bashPPCell{cell}}
		r.exit.returning = true
		return true
	}

	if cell, handled, err := r.goSourceChannelValueCell(expr); handled {
		if err != nil {
			r.bashPPGoSendError(expr, err)
			return true
		}
		r.bashPPReturn = bashPPReturnState{active: true, values: []string{cell.vr.String()}, cells: []*bashPPCell{cell}}
		r.exit.returning = true
		return true
	}
	// An imported value returned as itself — `return color.RGBAModel`,
	// `return image.Rect(…)` — crosses back as the authenticated native handle
	// the dependency owns, never flattened into text. Imported scalars keep
	// their existing scalar cells; see [goSourceNativeValueCell].
	if r.bashPPGoSource && r.bashPPNativeExpr(expr) {
		value, err := r.bashPPBridgeExpr(expr)
		if err != nil {
			// Native evaluation may already have recorded a Go panic or exit.
			// Keep that state so deferred recover and cancellation can unwind.
			if errors.Is(err, errBashPPScalarInterrupted) || r.bashPPPanicking() || r.exit.exiting || r.exit.fatalExit {
				return true
			}
			r.exit.fatal(&goSourceError{prefix: r.bashErrPrefix(expr.Pos()), err: err})
			r.bashPPShortFailureSeq++
			return true
		}
		cell := goSourceNativeValueCell(value)
		r.bashPPReturn = bashPPReturnState{active: true, values: []string{cell.vr.String()}, cells: []*bashPPCell{cell}}
		r.exit.returning = true
		return true
	}
	// A returned value need not be scalar: `return &V{…}`, `return *p` and
	// `return v.Inner` all name storage the caller receives as a value, and
	// the cell is the only thing that can carry it across the boundary.
	structured, structuredErr := r.bashPPStructuredArgCell(nil, expr)
	if structuredErr != nil {
		// `return x.(T)` whose assertion fails has raised a Go panic and
		// is unwinding; the interrupt is not a diagnostic, and reporting
		// it would exit 2 after a deferred recover has caught the panic.
		if errors.Is(structuredErr, errBashPPScalarInterrupted) || r.bashPPPanicking() || r.exit.exiting || r.exit.fatalExit {
			return true
		}
		r.errf("%v\n", structuredErr)
		r.exit = exitStatus{code: 2}
		r.bashPPShortFailureSeq++
		return true
	}
	if structured != nil {
		r.bashPPReturn = bashPPReturnState{active: true, values: []string{structured.vr.String()}, cells: []*bashPPCell{structured}}
		r.exit.returning = true
		return true
	}
	return false
}

// bashPPReturnScalarValue returns the scalar expr evaluates to.
func (r *Runner) bashPPReturnScalarValue(expr syntax.BashPPExpr) {
	value, err := r.bashPPEvalScalarExpr(expr)
	if err != nil {
		if errors.Is(err, errBashPPScalarInterrupted) {
			return
		}
		r.errf("%v\n", err)
		r.exit = exitStatus{code: 2}
		r.bashPPShortFailureSeq++
		return
	}
	text := bashPPScalarStorageString(value)
	cell := &bashPPCell{vr: expand.Variable{Set: true, Kind: expand.String, Str: text}, scalarKind: value.kind(), negativeZero: value.negativeZero, nonFinite: value.nonFinite, hasNonFinite: value.hasNonFinite, nonFiniteComplex: value.nonFiniteComplex, hasNonFiniteComplex: value.hasNonFiniteComplex}
	if value.typ != "" {
		cell.declType, cell.typeName = bashPPScalarNamedType(value.typ)
	}
	if conversion, ok := expr.(*syntax.BashPPConvertExpr); ok {
		if target := r.bashPPConvertTarget(conversion); target != nil {
			cell.declType = target
		}
	}
	r.bashPPReturn = bashPPReturnState{active: true, values: []string{text}, cells: []*bashPPCell{cell}}
	r.exit.returning = true
}

// bashPPPushDeferred records one prepared deferred call on the frame it belongs
// to. Ordinarily that is the live stack for the running invocation, but a defer
// executed directly inside a range-over-function body belongs to the ENCLOSING
// function, so it is diverted to the range sink and spliced onto the enclosing
// frame's region once the iterator has finished; see [Runner.bashPPRangeDefer]
// and [Runner.goSourceRangeFunction].
func (r *Runner) bashPPPushDeferred(entry bashPPDeferred) {
	if r.bashPPRangeDefer != nil {
		*r.bashPPRangeDefer = append(*r.bashPPRangeDefer, entry)
		return
	}
	r.bashPPDeferStack = append(r.bashPPDeferStack, entry)
}

// bashPPDeferStmt records a deferred call, evaluating its arguments now.
func (r *Runner) bashPPDeferStmt(ctx context.Context, d *syntax.BashPPDefer) {
	if d.Call == nil {
		return
	}
	if r.bashPPFuncActive == 0 {
		// The parser claims the call-shaped form because stock bash rejects it,
		// but defer has meaning only in a Go-form function body. Diagnose this
		// Class-R misuse instead of silently queuing a cleanup that can never run.
		r.errf("defer: not inside a Bash++ function\n")
		r.exit.code = 2
		return
	}
	// Both the function and its arguments are fixed HERE, as Go fixes them:
	// a literal is captured now, and a name resolves to the function it names
	// now, so a later rebinding cannot change which cleanup runs.
	entry := bashPPDeferred{call: d.Call, agentic: r.bashPPAgentic}
	if invoke, handled := r.goSourceCaptureDeferredClose(d.Call); handled {
		if invoke != nil {
			entry.builtin = invoke
			r.bashPPPushDeferred(entry)
		}
		return
	}
	if invoke, handled := r.bashPPTestingCapture(d.Call); handled {
		if invoke != nil {
			entry.testing = invoke
			r.bashPPPushDeferred(entry)
		}
		return
	}
	if invoke, handled, err := r.bashPPNativeCapture(ctx, d.Call); handled {
		if err != nil {
			if !r.bashPPPanicking() {
				r.exit.fatal(err)
			}
			return
		}
		entry.native = invoke
		r.bashPPPushDeferred(entry)
		return
	}
	if captured, cells, handled := r.goSourceCaptureDeferredValueBuiltin(d.Call); handled {
		if captured != nil {
			entry.call = captured
			entry.captured = cells
			entry.predeclared = bashPPPredeclaredCall(d.Call)
			r.bashPPPushDeferred(entry)
		}
		return
	}
	if fn, ok := r.bashPPLookupFunc(d.Call); ok {
		args, ok := r.bashPPCallValues(d.Call, fn)
		if !ok {
			return
		}
		entry.fn, entry.args = fn, args
		entry.cells = r.bashPPCallCells
		r.bashPPCallCells = nil
	} else if r.bashPPNilFuncCall(d.Call) {
		// The function value is fixed now and is nil; the run-time error is
		// raised when the deferred call runs, as in Go.
		entry.builtin = r.bashPPRaiseNilFuncCall
	} else {
		if r.exit.code != 0 {
			return
		}
		// A predeclared callee is resolved here for the same reason a declared
		// one is: what the defer names is fixed now, not at unwind time.
		entry.predeclared = bashPPPredeclaredCall(d.Call)
		entry.args = r.bashPPCallArgValues(d.Call)
	}
	r.bashPPPushDeferred(entry)
}

// bashPPRunDefers runs the deferred calls pushed above mark, most recent first,
// then trims the stack back to mark. A return in flight is paused across the
// defers and resumes afterwards, matching Go, where a deferred call runs even
// as the function is returning.
//
// A PANIC IN FLIGHT IS ALSO PAUSED, in a different sense: it stays recorded,
// because that is what a deferred call recovers, but the halt it imposes is
// lifted for the duration of each call, because otherwise no cleanup would run
// at all. If a deferred call recovers, the loop keeps going with the panic
// gone; if one panics itself, the new panic replaces the old and the remaining
// cleanups still run, exactly as Go keeps unwinding.
func (r *Runner) bashPPRunDefers(ctx context.Context, mark int) {
	// Copy before truncating: merely slicing would retain the backing array, so
	// a deferred call which itself defers could overwrite an entry we have not
	// run yet when append reuses that capacity.
	pending := append([]bashPPDeferred(nil), r.bashPPDeferStack[mark:]...)
	r.bashPPDeferStack = r.bashPPDeferStack[:mark]
	savedReturning := r.exit.returning
	r.exit.returning = false
	savedDeferDepth := r.bashPPDeferDepth
	savedAgentic := r.bashPPAgentic
	// A call this frame deferred runs one frame deeper than this one, and that
	// depth is the whole of recover's "called directly by a deferred function"
	// rule; see [Runner.bashPPRecover].
	r.bashPPDeferDepth = len(r.callStack) + 1
	// A frame that a deferred call of an OUTER unwinding frame invoked
	// entered with the halt lifted (running); its own defers must hand that
	// back, or the cleanup that called it stops at the call.
	savedRunning := r.bashPPPanic.running
	defer func() {
		r.bashPPAgentic = savedAgentic
		r.bashPPDeferDepth = savedDeferDepth
		r.bashPPPanic.running = savedRunning && r.bashPPPanic.active
	}()
	var failed exitStatus
	deferFailed := false
	var testingControl *goSourceTestingExit
	for i := len(pending) - 1; i >= 0; i-- {
		d := pending[i]
		r.bashPPAgentic = d.agentic
		r.exit = exitStatus{}
		// A cleanup runs even while a panic is unwinding — that is the whole
		// point of it — so the panic stops halting statements for the length
		// of this call, without ceasing to be recoverable by it. A panic
		// that abandoned this frame has unwound to it: a function the
		// cleanup calls is deeper and returns normally. A panic an outer
		// frame is unwinding — this frame being one of its cleanups — stays
		// that frame's.
		r.bashPPPanic.running = r.bashPPPanic.active
		if n := len(r.bashPPPanic.depths); r.bashPPPanic.active && n > 0 && r.bashPPPanic.depths[n-1] > len(r.callStack) {
			r.bashPPPanic.depths[n-1] = len(r.callStack)
		}
		builtinPanicDepth := len(r.bashPPPanic.chain)
		runDeferred := func() {
			switch {
			case d.builtin != nil:
				d.builtin()
			case d.native != nil:
				if err := d.native(ctx); err != nil && !r.bashPPPanicking() {
					r.exit.fatal(err)
				}
			case d.testing != nil:
				d.testing()
			case d.fn != nil:
				r.bashPPCallCells = d.cells
				r.bashPPInvoke(ctx, d.fn, d.args)
			case d.predeclared != "":
				// `defer panic(v)` and `defer recover()`. The latter in the
				// panicking frame itself is the shape Go documents as not
				// working, and it does not work here either: recover IS the
				// deferred call, so nothing deferred it in turn — see
				// [Runner.bashPPRecover]. Deferred by a function that is itself
				// a directly deferred call of the unwinding frame, it is that
				// function's own recover and does work, as in Go.
				if d.predeclared == "recover" && savedDeferDepth == len(r.callStack) {
					r.bashPPDeferDepth = savedDeferDepth
					r.bashPPPredeclared(d.predeclared, d.call, d.args)
					r.bashPPDeferDepth = len(r.callStack) + 1
				} else if bashPPValueBuiltin(d.predeclared) {
					// The captured argument cells are rebound in a scope of
					// their own: the block that declared them at defer time
					// may already have been popped.
					if d.captured != nil {
						saved := r.bashPPScope
						r.bashPPScope = newBashPPScope(saved)
						for name, cell := range d.captured {
							r.bashPPScope.put(name, cell)
						}
						r.bashPPRunValueBuiltin(d.predeclared, d.call)
						r.bashPPScope = saved
					} else {
						r.bashPPRunValueBuiltin(d.predeclared, d.call)
					}
				} else {
					r.bashPPPredeclared(d.predeclared, d.call, d.args)
				}
			case len(d.call.Fun) > 1:
				// A deferred SELECTOR is dispatched exactly as a direct one is,
				// through the import evaluator, so `defer fmt.Println(x)` reaches
				// the package rather than a shell command named after the final
				// selector element. Its arguments were evaluated at defer time and
				// are handed over as values, so the call the evaluator makes is the
				// one the defer described.
				r.bashPPEvalSelector(ctx, d.call, d.args)
			case len(d.call.Fun) > 0:
				// A deferred call to something that is not a typed function runs as an
				// ordinary command, which is what makes `defer log(...)` reach a shell
				// helper of that name.
				r.call(ctx, d.call.Pos(), append([]string{d.call.Fun[0].Value}, d.args...))
			}
		}
		if r.goSourceTesting != nil {
			if control := bashPPTestingCatch(runDeferred); control != nil {
				testingControl = control
			}
		} else {
			runDeferred()
		}
		// An explicit `exit` inside a cleanup terminates the script there and
		// then: the remaining cleanups do not run, and any panic in flight is
		// discarded rather than reported, the precedence `os.Exit` has over a
		// panic in Go.
		r.bashPPPanic.running = false
		if r.exit.exiting || r.exit.fatalExit {
			r.bashPPPanicSettledByExit()
			return
		}
		// Cleanup failures are observable. Keep the first failure in execution
		// order while still running every remaining defer, then restore the
		// enclosing function's return status when all cleanups succeeded.
		// A deferred call can start a recoverable panic — a GoSource builtin,
		// `defer panic(v)`, a function that panics. Its unwind status is not
		// a failed shell cleanup to restore after a later defer recovers; the
		// panic state carries that control transfer.
		// A status a call uses to REPORT its answer — `defer recover()` with
		// nothing to recover — is not a failed cleanup either.
		panicRaised := len(r.bashPPPanic.chain) > builtinPanicDepth
		if !deferFailed && !panicRaised && (!r.exit.ok() && !r.exit.errexitExempt || r.exit.err != nil) {
			failed, deferFailed = r.exit, true
		}
	}
	if testingControl != nil {
		panic(*testingControl)
	}
	if r.bashPPPanicking() {
		// The frame is still being abandoned; its status is the panic's, not
		// the last cleanup's.
		return
	}
	if deferFailed {
		r.exit = failed
	} else {
		r.exit.returning = savedReturning
	}
}

// bashPPParam is one parameter slot: the name it binds, the type it declares,
// and whether it is the variadic one. Flattening the field groups into slots
// once is what keeps the arity check, the binding loop and the diagnostics
// counting the same things.
type bashPPParam struct {
	name         string
	declared     string
	typ          syntax.BashPPTypeExpr
	variadic     bool
	defaultValue *syntax.Word
}

// bashppParams flattens a parameter list into one slot per declared name, plus
// slots for unnamed parameters, which accept arguments without binding them.
func bashppParams(fields []*syntax.BashPPField) []bashPPParam {
	var params []bashPPParam
	for _, f := range fields {
		declared := ""
		if f.FieldType != nil {
			declared = f.FieldType.Value
		}
		if f.Variadic() {
			name := ""
			if len(f.Names) > 0 {
				name = f.Names[0].Value
			}
			params = append(params, bashPPParam{name: name, declared: declared, typ: f.FieldTypeExpr, variadic: true})
			continue
		}
		if len(f.Names) == 0 {
			params = append(params, bashPPParam{declared: declared, typ: f.FieldTypeExpr})
			continue
		}
		for _, n := range f.Names {
			params = append(params, bashPPParam{name: n.Value, declared: declared, typ: f.FieldTypeExpr, defaultValue: f.Default})
		}
	}
	return params
}

func bashppHasDefaults(fields []*syntax.BashPPField) bool {
	for _, field := range fields {
		if field.Default != nil {
			return true
		}
	}
	return false
}

func bashppRequiredAfterDefault(fields []*syntax.BashPPField) string {
	sawDefault := false
	for _, field := range fields {
		if field.Default != nil {
			sawDefault = true
			continue
		}
		if sawDefault && !field.Variadic() && len(field.Names) > 0 {
			return field.Names[0].Value
		}
	}
	return ""
}

// bashppVariadic reports whether a parameter list ends in a `...T` group.
func bashppVariadic(fields []*syntax.BashPPField) bool {
	return len(fields) > 0 && fields[len(fields)-1].Variadic()
}

// bashPPCheckArgs applies the call's arity and type rules, reporting the first
// failure and returning false.
//
// ARITY. A non-variadic function takes exactly its parameters. A variadic one
// takes at least its fixed parameters and any number beyond them, including
// none — which is why the two are worded differently: "expected 2" and
// "expected at least 2" tell a reader which rule they broke.
//
// SPREAD. `f(xs...)` hands the variadic parameter one whole slice rather than
// one element per argument, so the trailing argument is checked against []T by
// the binding that receives it — see [Runner.goSourceBindSpreadVariadic] — and
// not against T here, which would reject every spread.
func (r *Runner) bashPPCheckArgs(fn *bashPPFunc, params []bashPPParam, args []string, spread bool) bool {
	fixed := len(params)
	variadic := fixed > 0 && params[fixed-1].variadic
	if variadic {
		fixed--
	}
	switch {
	case variadic && len(args) < fixed:
		r.errf("%s: expected at least %d argument(s), got %d\n", fn.name(), fixed, len(args))
		r.exit = exitStatus{code: 2}
		return false
	case !variadic && len(args) != fixed:
		r.errf("%s: expected %d argument(s), got %d\n", fn.name(), fixed, len(args))
		r.exit = exitStatus{code: 2}
		return false
	}
	for i, arg := range args {
		param := params[min(i, len(params)-1)]
		if spread && param.variadic {
			continue
		}
		if signature, ok := param.typ.(*syntax.BashPPFuncType); ok {
			if r.goSourceNilFuncArgument(arg) {
				continue
			}
			actual, found := r.bashPPClosure(arg)
			if !found || bashPPFieldsSignature(actual.params()) != bashPPFieldsSignature(signature.Params) || bashPPFieldsSignature(actual.results()) != bashPPFieldsSignature(signature.Results) {
				r.errf("BASHPP-EARG-FUNCTYPE: %s requires %s for parameter %s\n", fn.name(), bashPPTypeText(signature), param.name)
				r.exit = exitStatus{code: 2}
				return false
			}
			continue
		}
		if r.bashPPValueFits(param.declared, arg) {
			continue
		}
		where := param.name
		if where == "" {
			where = strconv.Itoa(i + 1)
		}
		r.errf("%s: cannot use %q as %s value for parameter %s\n",
			fn.name(), arg, param.declared, where)
		r.exit = exitStatus{code: 2}
		return false
	}
	return true
}

// bashPPValueFits reports whether value is admissible for a parameter declared
// with the given type.
//
// It is deliberately NARROW. Values in this dialect are strings, so a type is
// checked only where the string form has an unambiguous membership test: the
// numeric and boolean types, and `func`, whose values are the runner's own
// closure handles and therefore exactly recognizable. Every other spelling —
// `string`, a dotted selector, a name the script declared with `type` — is
// accepted, because guessing at a membership rule for it would reject values
// the phase has no way to construct an opinion about. An untyped parameter
// (`func f(v)`) declares nothing and so admits everything.
func (r *Runner) bashPPValueFits(declared, value string) bool {
	seen := make(map[string]bool)
	for {
		typ, ok := r.bashPPTypes[declared]
		if !ok {
			break
		}
		if seen[declared] {
			return false
		}
		seen[declared] = true
		if typ.underlying == "enum" {
			for _, member := range typ.members {
				if value == member {
					return true
				}
			}
			return false
		}
		declared = typ.underlying
	}
	switch declared {
	case "int":
		_, err := strconv.ParseInt(value, 10, strconv.IntSize)
		return err == nil
	case "int8":
		_, err := strconv.ParseInt(value, 10, 8)
		return err == nil
	case "int16":
		_, err := strconv.ParseInt(value, 10, 16)
		return err == nil
	case "int32", "rune":
		_, err := strconv.ParseInt(value, 10, 32)
		return err == nil
	case "int64":
		_, err := strconv.ParseInt(value, 10, 64)
		return err == nil
	case "uint", "uintptr":
		_, err := strconv.ParseUint(value, 10, strconv.IntSize)
		return err == nil
	case "uint8", "byte":
		_, err := strconv.ParseUint(value, 10, 8)
		return err == nil
	case "uint16":
		_, err := strconv.ParseUint(value, 10, 16)
		return err == nil
	case "uint32":
		_, err := strconv.ParseUint(value, 10, 32)
		return err == nil
	case "uint64":
		_, err := strconv.ParseUint(value, 10, 64)
		return err == nil
	case "float32":
		_, err := strconv.ParseFloat(value, 32)
		return err == nil
	case "float64":
		_, err := strconv.ParseFloat(value, 64)
		return err == nil
	case "complex64":
		_, err := strconv.ParseComplex(value, 64)
		return err == nil
	case "complex128":
		_, err := strconv.ParseComplex(value, 128)
		return err == nil
	case "bool":
		return value == "true" || value == "false"
	case "func":
		_, ok := r.bashPPClosure(value)
		return ok
	}
	return true
}

// bashppResultNames lists one entry per result slot: the name for a named
// result, and the empty string for an unnamed one.
func bashppResultNames(fields []*syntax.BashPPField) []string {
	var names []string
	for _, f := range fields {
		if len(f.Names) == 0 {
			names = append(names, "")
			continue
		}
		for _, n := range f.Names {
			names = append(names, n.Value)
		}
	}
	return names
}

func bashppResultTypes(fields []*syntax.BashPPField) []string {
	var types []string
	for _, f := range fields {
		declared := ""
		if f.FieldType != nil {
			declared = f.FieldType.Value
		}
		count := len(f.Names)
		if count == 0 {
			count = 1
		}
		for range count {
			types = append(types, declared)
		}
	}
	return types
}

func bashppResultTypeExprs(fields []*syntax.BashPPField) []syntax.BashPPTypeExpr {
	var types []syntax.BashPPTypeExpr
	for _, field := range fields {
		count := len(field.Names)
		if count == 0 {
			count = 1
		}
		for range count {
			types = append(types, field.FieldTypeExpr)
		}
	}
	return types
}

func bashPPSubstituteFields(fields []*syntax.BashPPField, typeArgs map[string]syntax.BashPPTypeExpr) []*syntax.BashPPField {
	if len(typeArgs) == 0 {
		return fields
	}
	out := make([]*syntax.BashPPField, len(fields))
	for i, field := range fields {
		cp := *field
		if field.FieldTypeExpr != nil {
			cp.FieldTypeExpr = bashPPSubstituteType(field.FieldTypeExpr, typeArgs)
			if field.FieldType != nil {
				cp.FieldType = &syntax.Lit{ValuePos: field.FieldType.Pos(), ValueEnd: field.FieldType.End(), Value: bashPPTypeText(cp.FieldTypeExpr)}
			}
		}
		out[i] = &cp
	}
	return out
}

func bashPPSubstituteType(typ syntax.BashPPTypeExpr, typeArgs map[string]syntax.BashPPTypeExpr) syntax.BashPPTypeExpr {
	switch x := typ.(type) {
	case *syntax.BashPPTypeParamType:
		if arg := typeArgs[x.Name.Value]; arg != nil {
			return arg
		}
	case *syntax.BashPPNamedType:
		// A type parameter used in a BODY statement is parsed as an ordinary
		// named type: the declaration recognizers never see the enclosing
		// parameter list, so only signature positions get the dedicated
		// BashPPTypeParamType marker. Resolving the name here is what makes
		// `var zero T` inside the body mean the same as `T` in the signature.
		// A Go type parameter shadows any same-named type for the whole
		// function, so a binding wins over the script's namespace.
		if len(x.TypeArgs) == 0 {
			if arg := typeArgs[x.Name.Value]; arg != nil {
				return arg
			}
		}
		cp := *x
		cp.TypeArgs = append([]*syntax.BashPPTypeArg(nil), x.TypeArgs...)
		for i, arg := range cp.TypeArgs {
			ac := *arg
			ac.ArgType = bashPPSubstituteType(ac.ArgType, typeArgs)
			cp.TypeArgs[i] = &ac
		}
		return &cp
	case *syntax.BashPPFuncType:
		cp := *x
		cp.Params = bashPPSubstituteFields(x.Params, typeArgs)
		cp.Results = bashPPSubstituteFields(x.Results, typeArgs)
		return &cp
	case *syntax.BashPPCollectionType:
		cp := *x
		cp.Key = bashPPSubstituteType(x.Key, typeArgs)
		cp.Element = bashPPSubstituteType(x.Element, typeArgs)
		return &cp
	case *syntax.BashPPPointerType:
		cp := *x
		cp.Element = bashPPSubstituteType(x.Element, typeArgs)
		return &cp
	case *syntax.BashPPStructType:
		cp := *x
		cp.Fields = bashPPSubstituteFields(x.Fields, typeArgs)
		return &cp
	case *syntax.BashPPInterfaceType:
		cp := *x
		cp.Elems = append([]*syntax.BashPPInterfaceElem(nil), x.Elems...)
		for i, elem := range cp.Elems {
			ec := *elem
			ec.Embedded = bashPPSubstituteType(ec.Embedded, typeArgs)
			if ec.Method != nil {
				mc := *ec.Method
				mc.Params = bashPPSubstituteFields(mc.Params, typeArgs)
				mc.Results = bashPPSubstituteFields(mc.Results, typeArgs)
				ec.Method = &mc
			}
			cp.Elems[i] = &ec
		}
		cp.Methods = append([]*syntax.BashPPMethodSpec(nil), x.Methods...)
		for i, method := range cp.Methods {
			mc := *method
			mc.Params = bashPPSubstituteFields(mc.Params, typeArgs)
			mc.Results = bashPPSubstituteFields(mc.Results, typeArgs)
			cp.Methods[i] = &mc
		}
		return &cp
	case *syntax.BashPPUnionType:
		cp := *x
		cp.Terms = append([]syntax.BashPPTypeExpr(nil), x.Terms...)
		for i, term := range cp.Terms {
			cp.Terms[i] = bashPPSubstituteType(term, typeArgs)
		}
		return &cp
	case *syntax.BashPPApproxType:
		cp := *x
		cp.Term = bashPPSubstituteType(cp.Term, typeArgs)
		return &cp
	case *syntax.BashPPChanType:
		// The element travels twice: as a type node and as the text the
		// channel checks compare against a channel's recorded element, so
		// both are rewritten or `chan T` would still read as `chan T`.
		cp := *x
		cp.Element = bashPPSubstituteType(x.Element, typeArgs)
		if cp.Element != x.Element && x.Elem != nil {
			elem := *x.Elem
			elem.Value = bashPPTypeText(cp.Element)
			cp.Elem = &elem
		}
		return &cp
	}
	return typ
}

// bashppResultCount is the number of values a function returns.
func bashppResultCount(fields []*syntax.BashPPField) int {
	count := 0
	for _, f := range fields {
		if len(f.Names) == 0 {
			count++
			continue
		}
		count += len(f.Names)
	}
	return count
}

// bashppExitCode parses a bash-style return status, which is taken modulo 256
// exactly as the shell's own `return` builtin does.
func bashppExitCode(s string) (uint8, bool) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return uint8(n), true
}

// goSourceNewCall recognizes the predeclared `new(T)` delivered as a call —
// the single-result return is the one place the Go front end does not lower
// it to a NewExpr — and rebuilds the allocation with the type its argument
// spells. A shadowed `new`, or a type spelling the call cannot carry, is
// left to the ordinary call path.
func (r *Runner) goSourceNewCall(call *syntax.BashPPCall) (*syntax.BashPPNewExpr, bool) {
	if !r.bashPPGoSource || bashPPPredeclaredCall(call) != "new" || len(call.ArgExprs) != 1 || call.ArgType != nil {
		return nil, false
	}
	if r.bashPPFuncs["new"] != nil || (r.bashPPScope != nil && r.bashPPScope.lookup("new") != nil) {
		return nil, false
	}
	typ, ok := goSourceExprType(call.ArgExprs[0])
	if !ok {
		return nil, false
	}
	return &syntax.BashPPNewExpr{New: call.Fun[0], Lparen: call.Lparen, Rparen: call.Rparen, AllocType: r.bashPPBindTypeExpr(typ)}, true
}

// goSourceExprType reads a type spelled as an expression: a name, `*T`, or
// an instantiation `G[A, B]` of names.
func goSourceExprType(expr syntax.BashPPExpr) (syntax.BashPPTypeExpr, bool) {
	switch x := expr.(type) {
	case *syntax.BashPPParenExpr:
		return goSourceExprType(x.X)
	case *syntax.BashPPIdent:
		return &syntax.BashPPNamedType{Name: x.Name}, true
	case *syntax.BashPPDerefExpr:
		elem, ok := goSourceExprType(x.X)
		if !ok {
			return nil, false
		}
		return &syntax.BashPPPointerType{Star: x.Star, Element: elem}, true
	case *syntax.BashPPIndexExpr:
		base, ok := x.X.(*syntax.BashPPIdent)
		if !ok {
			return nil, false
		}
		arg, ok := goSourceExprType(x.Index)
		if !ok {
			return nil, false
		}
		return &syntax.BashPPNamedType{Name: base.Name, TypeArgs: []*syntax.BashPPTypeArg{{ArgType: arg}}}, true
	}
	return nil, false
}
