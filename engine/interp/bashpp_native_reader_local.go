package interp

import "github.com/bashsharp/bashsharp/engine/syntax"

// This deliberately small escape proof accepts a range that only writes a
// literal into the byte parameter, followed by return len(parameter), nil.
// Every original statement still executes in the interpreter. No alias,
// receiver access, call, defer, concurrency, or early exit can escape a snapshot
// or observe its delayed writeback. All other bodies retain native handles.
func bashPPReaderLocalBufferProof(decl *syntax.BashPPFuncDecl) bool {
	if decl == nil || decl.Body == nil || len(decl.Body.Stmts) != 2 || len(decl.Params) != 1 || len(decl.Params[0].Names) != 1 {
		return false
	}
	param := decl.Params[0].Names[0].Value
	if param == "" || param == "_" || param == "len" || param == "nil" {
		return false
	}
	if decl.Receiver != nil && decl.Receiver.Name != nil && (decl.Receiver.Name.Value == "len" || decl.Receiver.Name.Value == "nil") {
		return false
	}
	for _, stmt := range []*syntax.Stmt{decl.Body.Stmts[0], decl.Body.Stmts[1]} {
		if stmt.Background || stmt.Negated || stmt.Coprocess || stmt.Disown || len(stmt.Redirs) > 0 {
			return false
		}
	}
	for _, field := range decl.Results {
		if len(field.Names) != 0 {
			return false
		}
	}
	ident := func(expr syntax.BashPPExpr, name string) bool {
		x, ok := expr.(*syntax.BashPPIdent)
		return ok && x.Name != nil && x.Name.Value == name
	}
	loop, ok := decl.Body.Stmts[0].Cmd.(*syntax.BashPPRange)
	if !ok || !loop.Define.IsValid() || len(loop.Names) != 1 || loop.Names[0].Value == "_" || loop.Names[0].Value == param || !ident(loop.Expr, param) || loop.Body == nil || len(loop.Body.Stmts) != 1 {
		return false
	}
	stmt := loop.Body.Stmts[0]
	if stmt.Background || stmt.Negated || stmt.Coprocess || stmt.Disown || len(stmt.Redirs) > 0 {
		return false
	}
	assign, ok := stmt.Cmd.(*syntax.BashPPAssign)
	if !ok || assign.Call != nil || len(assign.ValueExprs) > 1 {
		return false
	}
	index, ok := assign.TargetExpr.(*syntax.BashPPIndexExpr)
	if !ok || !ident(index.X, param) || !ident(index.Index, loop.Names[0].Value) {
		return false
	}
	rhs := assign.ValueExpr
	if conv, ok := rhs.(*syntax.BashPPConvertExpr); ok {
		if conv.ConvType == nil || (conv.ConvType.Value != "byte" && conv.ConvType.Value != "uint8") {
			return false
		}
		rhs = conv.X
	}
	literal, ok := rhs.(*syntax.BashPPBasicLit)
	if !ok || (literal.Kind != "INT" && literal.Kind != "CHAR") {
		return false
	}
	ret, ok := decl.Body.Stmts[1].Cmd.(*syntax.BashPPReturn)
	if !ok || len(ret.ResultExprs) != 2 || !ident(ret.ResultExprs[1], "nil") {
		return false
	}
	call, ok := ret.ResultExprs[0].(*syntax.BashPPCall)
	return ok && call.CalleeExpr == nil && call.FuncLit == nil && len(call.Fun) == 1 && call.Fun[0].Value == "len" && len(call.ArgExprs) == 1 && ident(call.ArgExprs[0], param) && !call.Ellipsis.IsValid() && len(call.TypeArgs) == 0
}

func (r *Runner) bashPPReaderLocalBufferAllowed(fn *bashPPFunc) bool {
	if fn == nil || !bashPPReaderLocalBufferProof(fn.decl) {
		return false
	}
	// The spelling len/nil alone is insufficient: a package may shadow either.
	if _, ok := r.bashPPTypes["byte"]; ok {
		return false
	}
	if _, ok := r.bashPPTypes["uint8"]; ok {
		return false
	}
	for _, name := range []string{"len", "nil"} {
		if r.bashPPFuncs[name] != nil || (fn.scope != nil && fn.scope.lookup(name) != nil) {
			return false
		}
	}
	return true
}
