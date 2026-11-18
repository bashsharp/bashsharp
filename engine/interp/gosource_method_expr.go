package interp

// Sprint: #153; Story: S153.4; Story-ID: e58cccba74f8
import (
	"fmt"
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// A METHOD EXPRESSION ON A DECLARED TYPE.
//
// `T.M` and `(*T).M` name a function whose first parameter is the receiver:
// Go defines them as `func(recv T, a1 P1, …) R { return recv.M(a1, …) }`.
// The Go front end lowers only the type-parameter receiver that way; a
// declared type's method expression arrives as the selector it was written
// as, and the shell-form resolver bound its receiver from the FIRST ARGUMENT
// WORD at lookup time. A Go argument is an expression that must be evaluated
// exactly once, and in `(*p).h(x.f())` it is a call whose results are also
// spread; the closure defers all of that to one ordinary call.

// goSourceMethodExprType reports the receiver type a selector operand names
// as a method expression: T for `T.M`, *T for `(*T).M`, when T is a declared
// local type that no variable shadows. Any other operand is not one.
func (r *Runner) goSourceMethodExprType(x syntax.BashPPExpr) (syntax.BashPPTypeExpr, bool) {
	if !r.bashPPGoSource || r.bashPPScope == nil {
		return nil, false
	}
	pointer := false
	for {
		switch v := x.(type) {
		case *syntax.BashPPParenExpr:
			x = v.X
		case *syntax.BashPPDerefExpr:
			if pointer {
				return nil, false
			}
			pointer, x = true, v.X
		case *syntax.BashPPIdent:
			name := v.Name.Value
			// `error.Error(err)` is a method expression on the predeclared
			// error interface, which no type table lists.
			if _, declared := r.bashPPTypes[name]; !declared && name != "error" {
				return nil, false
			}
			// A type declaration leaves a marker cell of its own; a variable
			// of the same name carries a value type, and it wins.
			if cell := r.bashPPScope.lookup(name).view(); cell != nil && (cell.interfaceValue != nil || bashPPSelectorCellType(cell) != nil) {
				return nil, false
			}
			var typ syntax.BashPPTypeExpr = &syntax.BashPPNamedType{Name: v.Name}
			if pointer {
				typ = &syntax.BashPPPointerType{Element: typ}
			}
			return typ, true
		default:
			return nil, false
		}
	}
}

// goSourceMethodExprSelector reports whether a selector is a method
// expression `T.M` or `(*T).M` used as a value rather than a bound method
// value or a field read. The value paths route it to the forwarding closure
// (goSourceCallableCell) instead of reading its `(*T)` operand as a pointer.
func (r *Runner) goSourceMethodExprSelector(sel *syntax.BashPPSelectorExpr) bool {
	if !r.bashPPGoSource || sel.MethodValue {
		return false
	}
	_, ok := r.goSourceMethodExprType(sel.X)
	return ok
}

// goSourceMethodExprCallee reports the method expression a call's callee
// spells, in either of its two arrivals: `T.M(...)` comes as the two-part
// name, `(*T).M(...)` as a computed callee.
func (r *Runner) goSourceMethodExprCallee(c *syntax.BashPPCall) (syntax.BashPPTypeExpr, *syntax.Lit, bool) {
	if c.CalleeExpr != nil {
		sel, ok := c.CalleeExpr.(*syntax.BashPPSelectorExpr)
		if !ok || sel.MethodValue {
			return nil, nil, false
		}
		recv, ok := r.goSourceMethodExprType(sel.X)
		return recv, sel.Sel, ok
	}
	if len(c.Fun) != 2 || len(c.TypeArgs) > 0 {
		return nil, nil, false
	}
	recv, ok := r.goSourceMethodExprType(&syntax.BashPPIdent{Name: c.Fun[0]})
	return recv, c.Fun[1], ok
}

// goSourceMethodExprClosure builds the forwarding closure for method M of
// the receiver type: its parameters are the receiver followed by M's own,
// its results M's, and its body the one call `recv.M(args…)`.
func (r *Runner) goSourceMethodExprClosure(recv syntax.BashPPTypeExpr, method *syntax.Lit) (*bashPPFunc, error) {
	closure, err := r.goSourceMethodExprLit(recv, method)
	if err != nil {
		return nil, err
	}
	fn, _ := r.bashPPMakeClosure(closure)
	return fn, nil
}

// goSourceMethodExprLit is the forwarding function literal of
// goSourceMethodExprClosure, before it is bound to a scope.
func (r *Runner) goSourceMethodExprLit(recv syntax.BashPPTypeExpr, method *syntax.Lit) (*syntax.BashPPFuncLit, error) {
	sel := r.bashPPResolveSelection(recv, method.Value, true, false)
	if sel.ambiguous {
		return nil, fmt.Errorf("BASHPP-ESELECTOR-AMBIGUOUS: ambiguous selector %s.%s", bashPPTypeText(recv), method.Value)
	}
	var params, results []*syntax.BashPPField
	switch {
	case sel.method != nil:
		params, results = sel.method.decl.Params, sel.method.decl.Results
	case sel.interfaceSpec != nil:
		params, results = sel.interfaceSpec.Params, sel.interfaceSpec.Results
	default:
		return nil, fmt.Errorf("%s.%s undefined (type %s has no method %s)", bashPPTypeText(recv), method.Value, bashPPTypeText(recv), method.Value)
	}
	at := method.Pos()
	lit := func(text string) *syntax.Lit { return &syntax.Lit{ValuePos: at, ValueEnd: at, Value: text} }
	call := &syntax.BashPPCall{Lparen: at, Rparen: at}
	closure := &syntax.BashPPFuncLit{Kw: lit("func"), Lparen: at, Rparen: at}
	// The receiver parameter is named outside the identifier space of any Go
	// source, so no method parameter can shadow it.
	recvName := "__gosource_recv"
	closure.Params = append(closure.Params, &syntax.BashPPField{Names: []*syntax.Lit{lit(recvName)}, FieldType: lit(bashPPTypeText(recv)), FieldTypeExpr: recv})
	call.Fun = []*syntax.Lit{lit(recvName), method}
	var spelled []string
	for _, group := range params {
		count := max(len(group.Names), 1)
		for range count {
			name := fmt.Sprintf("__gosource_arg%d", len(spelled))
			field := *group
			field.Names = []*syntax.Lit{lit(name)}
			closure.Params = append(closure.Params, &field)
			text := name
			if field.Variadic() {
				text += "..."
				call.Ellipsis = at
			}
			spelled = append(spelled, text)
			call.Args = append(call.Args, &syntax.Word{Parts: []syntax.WordPart{lit(text)}})
			call.ArgExprs = append(call.ArgExprs, &syntax.BashPPIdent{Name: lit(name)})
		}
	}
	for _, group := range results {
		field := *group
		field.Names = nil
		closure.Results = append(closure.Results, &field)
	}
	body := &syntax.Block{Lbrace: at, Rbrace: at}
	if len(closure.Results) == 0 {
		body.Stmts = []*syntax.Stmt{{Cmd: call, Position: at}}
	} else {
		spelling := recvName + "." + method.Value + "(" + strings.Join(spelled, ", ") + ")"
		ret := &syntax.BashPPReturn{Kw: lit("return"), Call: call, Results: []*syntax.Word{{Parts: []syntax.WordPart{lit(spelling)}}}}
		body.Stmts = []*syntax.Stmt{{Cmd: ret, Position: at}}
	}
	closure.Body = body
	return closure, nil
}

// goSourceMethodExprCell is the function value a method expression used as
// a value denotes, typed with the signature the front end recorded. It
// reports whether the expression was a method expression at all.
func (r *Runner) goSourceMethodExprCell(expr syntax.BashPPExpr) (*bashPPCell, bool, error) {
	sel, ok := expr.(*syntax.BashPPSelectorExpr)
	if !ok || sel.MethodValue {
		return nil, false, nil
	}
	recv, ok := r.goSourceMethodExprType(sel.X)
	if !ok {
		return nil, false, nil
	}
	fn, err := r.goSourceMethodExprClosure(recv, sel.Sel)
	if err != nil {
		return nil, true, err
	}
	return &bashPPCell{vr: r.bashPPStoreFunc(fn), declType: sel.FuncType}, true, nil
}
