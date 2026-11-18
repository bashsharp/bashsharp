package gosource

import (
	"go/ast"
	"go/token"
	"go/types"

	s "github.com/bashsharp/bashsharp/engine/syntax"
)

// The initial sync proof is package-wide: one uncertain boundary keeps every
// synchronization allocation in the worker. No migration of live state occurs.
func syncTypeName(t types.Type) string {
	if t == nil {
		return ""
	}
	n, ok := types.Unalias(t).(*types.Named)
	if !ok || n.Obj().Pkg() == nil || n.Obj().Pkg().Path() != "sync" {
		return ""
	}
	switch n.Obj().Name() {
	case "Mutex", "RWMutex", "WaitGroup":
		return "sync." + n.Obj().Name()
	}
	return ""
}

func hasSyncType(t types.Type, seen map[types.Type]bool) bool {
	if t == nil || seen[t] {
		return false
	}
	seen[t] = true
	if syncTypeName(t) != "" {
		return true
	}
	switch x := t.Underlying().(type) {
	case *types.Pointer:
		return hasSyncType(x.Elem(), seen)
	case *types.Array:
		return hasSyncType(x.Elem(), seen)
	case *types.Slice:
		return hasSyncType(x.Elem(), seen)
	case *types.Chan:
		return hasSyncType(x.Elem(), seen)
	case *types.Map:
		return hasSyncType(x.Key(), seen) || hasSyncType(x.Elem(), seen)
	case *types.Struct:
		for i := 0; i < x.NumFields(); i++ {
			if hasSyncType(x.Field(i).Type(), seen) {
				return true
			}
		}
	case *types.Tuple:
		for i := 0; i < x.Len(); i++ {
			if hasSyncType(x.At(i).Type(), seen) {
				return true
			}
		}
	case *types.Signature:
		return hasSyncType(x.Params(), seen) || hasSyncType(x.Results(), seen)
	}
	return false
}

func syncMethod(o types.Object) bool {
	f, ok := o.(*types.Func)
	if !ok {
		return false
	}
	sig := f.Type().(*types.Signature)
	if sig.Recv() == nil {
		return false
	}
	t := sig.Recv().Type()
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	switch syncTypeName(t) {
	case "sync.Mutex":
		return f.Name() == "Lock" || f.Name() == "Unlock" || f.Name() == "TryLock"
	case "sync.RWMutex":
		return f.Name() == "RLock" || f.Name() == "RUnlock" || f.Name() == "Lock" || f.Name() == "Unlock" || f.Name() == "TryLock" || f.Name() == "TryRLock"
	case "sync.WaitGroup":
		return f.Name() == "Add" || f.Name() == "Done" || f.Name() == "Wait"
	}
	return false
}

// waitGroupGo reports sync.WaitGroup.Go, which the planner admits only as a
// statement the converter can lower to Add + go + Done (see waitGroupGoStmt).
func waitGroupGo(o types.Object) bool {
	f, ok := o.(*types.Func)
	if !ok || f.Name() != "Go" {
		return false
	}
	sig := f.Type().(*types.Signature)
	if sig.Recv() == nil {
		return false
	}
	t := sig.Recv().Type()
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	return syncTypeName(t) == "sync.WaitGroup"
}

// waitGroupGoStmt matches `x.Go(f)` as a whole statement whose receiver is a
// plain identifier/field chain. The lowering captures the receiver before
// evaluating f, then uses the captured receiver inside the launched closure.
func (c *converter) waitGroupGoStmt(call *ast.CallExpr) bool {
	sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr)
	if !ok || len(call.Args) != 1 || call.Ellipsis.IsValid() {
		return false
	}
	s := c.info.Selections[sel]
	if s == nil || s.Kind() != types.MethodVal || !waitGroupGo(s.Obj()) {
		return false
	}
	var chain func(ast.Expr) bool
	chain = func(e ast.Expr) bool {
		switch x := e.(type) {
		case *ast.Ident:
			_, isVar := c.info.ObjectOf(x).(*types.Var)
			return isVar
		case *ast.SelectorExpr:
			s := c.info.Selections[x]
			return s != nil && s.Kind() == types.FieldVal && chain(x.X)
		}
		return false
	}
	return chain(sel.X)
}

func (c *converter) localSyncType(t types.Type) string {
	name := syncTypeName(t)
	if name == "" {
		return ""
	}
	if !c.syncPlanned {
		c.planLocalSync()
	}
	if c.syncLocal {
		return name
	}
	return ""
}

func (c *converter) planLocalSync() {
	c.syncPlanned = true
	safe := true
	direct := map[*ast.SelectorExpr]bool{}
	has := func(t types.Type) bool { return hasSyncType(t, map[types.Type]bool{}) }
	// Native descriptors currently preserve identity on value assignment. Keep
	// value-copy programs on the existing path until copy codecs are supported.
	copied := func(e ast.Expr) bool {
		if c.info.Types[e].IsType() {
			return false
		}
		t := c.info.TypeOf(e)
		if t == nil {
			return false
		}
		if _, ok := t.Underlying().(*types.Pointer); ok {
			return false
		}
		if _, ok := ast.Unparen(e).(*ast.CompositeLit); ok {
			return false
		}
		return has(t)
	}
	for _, f := range c.files {
		ast.Inspect(f, func(n ast.Node) bool {
			if st, ok := n.(*ast.ExprStmt); ok {
				if call, ok := ast.Unparen(st.X).(*ast.CallExpr); ok && c.waitGroupGoStmt(call) {
					direct[ast.Unparen(call.Fun).(*ast.SelectorExpr)] = true
				}
			}
			call, ok := n.(*ast.CallExpr)
			if ok {
				if s, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr); ok {
					if sel := c.info.Selections[s]; sel != nil && sel.Kind() == types.MethodVal && syncMethod(sel.Obj()) {
						direct[s] = true
					}
				}
			}
			return true
		})
	}
	// A local variable is a statically known callee only when every binding
	// is a function literal (the planner inspects that body) and every other
	// use is as the callee of a call: never assigned an unknown value, address-taken, passed,
	// stored or returned, so it cannot escape.
	litVars, otherVars := map[*types.Var]bool{}, map[*types.Var]bool{}
	defs, callees := map[*ast.Ident]bool{}, map[*ast.Ident]bool{}
	bind := func(lhs, rhs ast.Expr) {
		id, ok := ast.Unparen(lhs).(*ast.Ident)
		if !ok {
			return
		}
		v, ok := c.info.ObjectOf(id).(*types.Var)
		if !ok {
			return
		}
		defs[id] = true
		if _, lit := ast.Unparen(rhs).(*ast.FuncLit); lit {
			litVars[v] = true
		} else {
			otherVars[v] = true
		}
	}
	for _, f := range c.files {
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.AssignStmt:
				for i, l := range x.Lhs {
					if len(x.Lhs) == len(x.Rhs) {
						bind(l, x.Rhs[i])
					} else {
						bind(l, nil)
					}
				}
			case *ast.ValueSpec:
				for i, id := range x.Names {
					if len(x.Names) == len(x.Values) {
						bind(id, x.Values[i])
					} else {
						bind(id, nil)
					}
				}
			case *ast.RangeStmt:
				if x.Key != nil {
					bind(x.Key, nil)
				}
				if x.Value != nil {
					bind(x.Value, nil)
				}
			case *ast.IncDecStmt:
				bind(x.X, nil)
			case *ast.CallExpr:
				if id, ok := ast.Unparen(x.Fun).(*ast.Ident); ok {
					callees[id] = true
				}
			}
			return true
		})
	}
	for _, f := range c.files {
		ast.Inspect(f, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && !defs[id] && !callees[id] {
				if v, ok := c.info.Uses[id].(*types.Var); ok {
					otherVars[v] = true
				}
			}
			return true
		})
	}
	localCallee := func(fun ast.Expr) bool {
		id, ok := ast.Unparen(fun).(*ast.Ident)
		if !ok {
			return false
		}
		v, ok := c.info.ObjectOf(id).(*types.Var)
		return ok && litVars[v] && !otherVars[v]
	}
	for _, f := range c.files {
		ast.Inspect(f, func(n ast.Node) bool {
			if e, ok := n.(ast.Expr); ok && channelOpaqueStorage(c.info.TypeOf(e), map[types.Type]bool{}) {
				safe = false
			}
			switch x := n.(type) {
			case *ast.BinaryExpr:
				if has(c.info.TypeOf(x.X)) || has(c.info.TypeOf(x.Y)) {
					safe = false
				}
			case *ast.FuncType:
				// Value receivers/parameters/results need copy codecs too.
				for _, fields := range []*ast.FieldList{x.Params, x.Results} {
					if fields != nil {
						for _, f := range fields.List {
							t := c.info.TypeOf(f.Type)
							if t != nil {
								if _, ptr := t.Underlying().(*types.Pointer); !ptr && has(t) {
									safe = false
								}
							}
						}
					}
				}
			case *ast.AssignStmt:
				for _, e := range x.Rhs {
					if copied(e) {
						safe = false
					}
				}
			case *ast.ValueSpec:
				for _, e := range x.Values {
					if copied(e) {
						safe = false
					}
				}
			case *ast.ReturnStmt:
				for _, e := range x.Results {
					if copied(e) {
						safe = false
					}
				}
			case *ast.SendStmt:
				if copied(x.Value) {
					safe = false
				}
			case *ast.CompositeLit:
				for _, e := range x.Elts {
					if kv, ok := e.(*ast.KeyValueExpr); ok {
						e = kv.Value
					}
					if copied(e) {
						safe = false
					}
				}
			case *ast.RangeStmt:
				if has(c.info.TypeOf(x.X)) {
					safe = false
				}
			case *ast.FuncDecl:
				if x.Recv != nil {
					for _, f := range x.Recv.List {
						t := c.info.TypeOf(f.Type)
						if t != nil {
							if _, ptr := t.Underlying().(*types.Pointer); !ptr && has(t) {
								safe = false
							}
						}
					}
				}
				if x.Body == nil {
					safe = false
				}
			case *ast.Ident:
				o := c.info.ObjectOf(x)
				if o != nil && o.Pkg() != nil && o.Pkg().Path() != c.packagePath {
					if o.Pkg().Path() == "unsafe" {
						safe = false
					}
					if _, ok := o.(*types.TypeName); !ok && !syncMethod(o) && has(o.Type()) {
						safe = false
					}
				}
			case *ast.SelectorExpr:
				if sel := c.info.Selections[x]; sel != nil && syncMethod(sel.Obj()) && !direct[x] {
					safe = false
				}
			case *ast.CallExpr:
				if id, ok := ast.Unparen(x.Fun).(*ast.Ident); ok {
					if builtin, ok := c.info.ObjectOf(id).(*types.Builtin); ok && builtin.Name() != "new" {
						for _, a := range x.Args {
							if has(c.info.TypeOf(a)) {
								safe = false
							}
						}
					}
				}
				for _, a := range x.Args {
					if copied(a) {
						safe = false
					}
				}
				if s, ok := ast.Unparen(x.Fun).(*ast.SelectorExpr); ok && direct[s] {
					break
				}
				if !c.localChannelCall(x.Fun) {
					if has(c.info.TypeOf(x)) {
						safe = false
					}
					if s, ok := ast.Unparen(x.Fun).(*ast.SelectorExpr); ok {
						if has(c.info.TypeOf(s.X)) {
							safe = false
						}
					}
					for _, a := range x.Args {
						if has(c.info.TypeOf(a)) || channelCallbackType(c.info.TypeOf(a), c.packagePath) {
							safe = false
						}
					}
					// A statically resolved function/method is a known boundary,
					// and so is a local variable proven to hold only inspected
					// function literals. Other indirect calls can expose
					// captured globals.
					known := false
					if sel, ok := ast.Unparen(x.Fun).(*ast.SelectorExpr); ok {
						if selection := c.info.Selections[sel]; selection != nil {
							_, known = selection.Obj().(*types.Func)
						} else {
							_, known = c.info.ObjectOf(sel.Sel).(*types.Func)
						}
					}
					if !known && !localCallee(x.Fun) {
						safe = false
					}
				}
			}
			return true
		})
	}
	c.syncLocal = safe
}

func waitGroupGoRecv(sel *types.Selection) types.Type {
	t := sel.Obj().(*types.Func).Type().(*types.Signature).Recv().Type()
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	return t
}

// lowerWaitGroupGo lowers a certified `wg.Go(f)` statement to Go's own
// definition, with Go's evaluation order, as
//
//	func(wgr *T, wgf func()) { wgr.Add(1); go func(){ defer wgr.Done(); wgf() }() }(&wg, f)
//
// The receiver (its address, or the pointer itself) and f are evaluated once,
// receiver first, as call arguments; Add runs only after both evaluated, and
// Done uses the captured receiver even if the variable or field is reassigned
// after the call. The goroutine runs in the interpreter against the resident
// WaitGroup instead of calling back through the native bridge.
func (c *converter) lowerWaitGroupGo(call *ast.CallExpr) []*s.Stmt {
	base := c.call(call)
	sel := ast.Unparen(call.Fun).(*ast.SelectorExpr)
	at := call.Pos()
	c.syntheticPos = at
	defer func() { c.syntheticPos = token.NoPos }()
	recvType := c.info.TypeOf(sel.X)
	recvText := c.text(sel.X)
	recvExpr := sel.X
	if _, ok := recvType.Underlying().(*types.Pointer); !ok {
		recvType = types.NewPointer(recvType)
		address := &ast.UnaryExpr{OpPos: sel.X.Pos(), Op: token.AND, X: sel.X}
		c.info.Types[address] = types.TypeAndValue{Type: recvType}
		recvExpr = address
		recvText = "&" + recvText
	}
	recvName, fname := c.prefix+"wgr", c.prefix+"wgf"
	call0 := func(fun ...string) *s.BashPPCall {
		var lits []*s.Lit
		for _, f := range fun {
			lits = append(lits, c.lit(at, f))
		}
		return &s.BashPPCall{GoRuntime: true, Fun: lits, Lparen: c.pos(at), Rparen: c.pos(at)}
	}
	add := call0(recvName, "Add")
	add.Args = []*s.Word{{Parts: []s.WordPart{c.lit(at, "1")}}}
	add.ArgExprs = []s.BashPPExpr{&s.BashPPBasicLit{Kind: token.INT.String(), Value: c.lit(at, "1")}}
	inner := &s.BashPPFuncLit{Kw: c.lit(at, "func"), Lparen: c.pos(at), Rparen: c.pos(at),
		Body: &s.Block{Lbrace: c.pos(at), Rbrace: c.pos(at), Stmts: []*s.Stmt{
			c.stmt(&s.BashPPDefer{Kw: c.lit(at, "defer"), Call: call0(recvName, "Done")}),
			c.stmt(call0(fname)),
		}}}
	goInner := &s.BashPPCall{GoRuntime: true, FuncLit: inner, Lparen: c.pos(at), Rparen: c.pos(at)}
	fn := types.NewSignatureType(nil, nil, nil, nil, nil, false)
	params := []*s.BashPPField{
		{Names: []*s.Lit{c.lit(at, recvName)}, FieldType: c.lit(at, c.typeString(recvType)), FieldTypeExpr: c.checkedType(recvType, call, "WaitGroup.Go receiver type")},
		{Names: []*s.Lit{c.lit(at, fname)}, FieldType: c.lit(at, "func()"), FieldTypeExpr: c.checkedType(fn, call, "WaitGroup.Go function type")},
	}
	outer := &s.BashPPFuncLit{Kw: c.lit(at, "func"), Params: params, Lparen: c.pos(at), Rparen: c.pos(at),
		Body: &s.Block{Lbrace: c.pos(at), Rbrace: c.pos(at), Stmts: []*s.Stmt{
			c.stmt(add),
			c.stmt(&s.BashPPGo{Kw: c.lit(at, "go"), Call: goInner}),
		}}}
	recvWord := &s.Word{Parts: []s.WordPart{c.lit(at, recvText)}}
	invoke := &s.BashPPCall{GoRuntime: true, FuncLit: outer, Lparen: base.Lparen, Rparen: base.Rparen,
		Args:               append([]*s.Word{recvWord}, base.Args...),
		ArgExprs:           append([]s.BashPPExpr{c.expr(recvExpr)}, base.ArgExprs...),
		ExclusiveSliceArgs: nil}
	return []*s.Stmt{c.stmt(invoke)}
}
