package gosource

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"

	s "github.com/bashsharp/bashsharp/engine/syntax"
)

// tupleValueDecls evaluates a checked multi-result initializer exactly once.
// Temporaries use the collision-free package prefix and bind before the real
// declarations, preserving the Go rule that a local variable's scope begins
// at the end of its ValueSpec. Each actual declaration keeps its checked type,
// including an explicit type for the otherwise untyped comma-ok boolean.
func (c *converter) tupleValueDecls(g *ast.GenDecl, v *ast.ValueSpec) []*s.Stmt {
	temps := make([]ast.Expr, len(v.Names))
	for i, name := range v.Names {
		temps[i] = &ast.Ident{NamePos: name.Pos(), Name: fmt.Sprintf("%stuple_%d_%d", c.prefix, v.Pos(), i)}
	}
	var out []*s.Stmt
	rhs := []ast.Expr{ast.Unparen(v.Values[0])}
	// Assertions consume an interface cell. Materialize a computed operand
	// once before the assertion rather than evaluating its call as a scalar.
	if assertion, ok := rhs[0].(*ast.TypeAssertExpr); ok {
		copy := *assertion
		copy.X = ast.Unparen(assertion.X)
		if _, simple := copy.X.(*ast.Ident); !simple {
			operand := &ast.Ident{NamePos: assertion.X.Pos(), Name: fmt.Sprintf("%stuple_operand_%d", c.prefix, v.Pos())}
			out = append(out, c.statements(&ast.AssignStmt{Lhs: []ast.Expr{operand}, TokPos: v.Pos(), Tok: token.DEFINE, Rhs: []ast.Expr{copy.X}})...)
			copy.X = operand
		}
		rhs = []ast.Expr{&copy}
	}
	out = append(out, c.statements(&ast.AssignStmt{Lhs: temps, TokPos: v.Pos(), Tok: token.DEFINE, Rhs: rhs})...)
	zero := *v
	zero.Values = nil
	for i, name := range v.Names {
		if name.Name == "_" {
			// Consume the temporary without declaring a blank binding. This
			// also keeps retained generic Go bodies valid (no unused temps).
			out = append(out, c.statements(&ast.AssignStmt{Lhs: []ast.Expr{name}, TokPos: name.Pos(), Tok: token.ASSIGN, Rhs: []ast.Expr{temps[i]}})...)
			continue
		}
		decl := c.valueDecl(g, &zero, name, i)
		decl.Init = []*s.Word{c.word(temps[i])}
		decl.InitExpr = c.expr(temps[i])
		if i == 1 && c.info.Types[ast.Unparen(v.Values[0])].HasOk() {
			// Restore the checked boolean identity even on runtime paths
			// whose comma-ok temporary stores only its textual value.
			name := c.lit(v.Pos(), "bool")
			var typ s.BashPPTypeExpr = &s.BashPPNamedType{Name: name}
			if v.Type != nil && tupleBooleanType(c.info.TypeOf(v.Type)) {
				name = c.lit(v.Type.Pos(), c.text(v.Type))
				typ = c.typ(v.Type)
			}
			decl.InitExpr = &s.BashPPConvertExpr{ConvType: name, ConvTypeExpr: typ, Lparen: c.pos(v.Pos()), Rparen: c.pos(v.End()), X: decl.InitExpr}
		}
		out = append(out, c.stmt(decl))
	}
	return out
}

func tupleBooleanType(typ types.Type) bool {
	if _, ok := typ.(*types.TypeParam); ok {
		return true
	}
	basic, ok := typ.Underlying().(*types.Basic)
	return ok && basic.Kind() == types.Bool
}

// tupleAssignStmts lowers a multi-target `=` whose left-hand side is not all
// plain identifiers — `p.a, xs[1] = f()`, `*p, ok = m[k]`.
//
// The results are bound to collision-free temporaries first and then assigned
// to their real targets one at a time, which is the shape every later phase
// already supports: one target, one value. Without it the whole family was
// refused at conversion, so a Go original could not assign a call's results
// into a field, an element or through a pointer at all.
//
// Go evaluates the operands of index expressions and pointer indirections on
// the left together with the right-hand operands, all in source order (the
// left side is textually before the right), then assigns left to right. The
// split therefore binds each target's addressing operands to temporaries
// first — before the right-hand side — so a left-hand call runs ahead of a
// right-hand one, matching Go. A pointer indirection binds its pointer, an
// index into a reference (map, slice or pointer-to-array) binds that reference
// and the key, and an index or selector into a value aggregate binds that
// aggregate's own operands in place (it cannot be copied without losing the
// write-back). Plain identifiers have no operand to evaluate. The right-hand
// side is then evaluated once into result temporaries which are assigned to the
// captured targets left to right.
func (c *converter) tupleAssignStmts(x *ast.AssignStmt) []*s.Stmt {
	var out []*s.Stmt
	n := 0
	targets := make([]ast.Expr, len(x.Lhs))
	for i, lhs := range x.Lhs {
		targets[i] = c.captureTarget(ast.Unparen(lhs), x.TokPos, &n, &out)
	}
	temps := make([]ast.Expr, len(x.Lhs))
	for i, lhs := range x.Lhs {
		temps[i] = &ast.Ident{NamePos: ast.Unparen(lhs).Pos(), Name: fmt.Sprintf("%stuple_%d_%d", c.prefix, x.TokPos, i)}
	}
	out = append(out, c.statements(&ast.AssignStmt{Lhs: temps, TokPos: x.TokPos, Tok: token.DEFINE, Rhs: x.Rhs})...)
	for i := range x.Lhs {
		out = append(out, c.statements(&ast.AssignStmt{Lhs: []ast.Expr{targets[i]}, TokPos: x.TokPos, Tok: token.ASSIGN, Rhs: []ast.Expr{temps[i]}})...)
	}
	return out
}

// captureTarget rewrites an assignment target so its addressing operands are
// evaluated, in source order, by the temporary-binding statements appended to
// out. Reference targets (a pointer indirection, or an index into a map, a
// slice or a pointer-to-array) are captured by binding the reference itself, so
// the later assignment still writes through to the original. A value aggregate
// (a struct or array the target selects or indexes into) cannot be copied to a
// temporary without losing the write-back, so its own operands are captured in
// place instead. A plain identifier has no operand to evaluate.
func (c *converter) captureTarget(target ast.Expr, at token.Pos, n *int, out *[]*s.Stmt) ast.Expr {
	bind := func(e ast.Expr, kind string) *ast.Ident {
		id := &ast.Ident{NamePos: e.Pos(), Name: fmt.Sprintf("%stuple_%s_%d_%d", c.prefix, kind, at, *n)}
		*n++
		*out = append(*out, c.statements(&ast.AssignStmt{Lhs: []ast.Expr{id}, TokPos: at, Tok: token.DEFINE, Rhs: []ast.Expr{e}})...)
		return id
	}
	switch t := target.(type) {
	case *ast.StarExpr:
		return &ast.StarExpr{Star: t.Star, X: bind(t.X, "ptr")}
	case *ast.SelectorExpr:
		if c.isPointerType(t.X) {
			return &ast.SelectorExpr{X: bind(t.X, "base"), Sel: t.Sel}
		}
		return &ast.SelectorExpr{X: c.captureTarget(t.X, at, n, out), Sel: t.Sel}
	case *ast.IndexExpr:
		if c.isReferenceType(t.X) {
			base := bind(t.X, "base")
			idx := bind(t.Index, "key")
			return &ast.IndexExpr{X: base, Lbrack: t.Lbrack, Index: idx, Rbrack: t.Rbrack}
		}
		base := c.captureTarget(t.X, at, n, out)
		idx := bind(t.Index, "key")
		return &ast.IndexExpr{X: base, Lbrack: t.Lbrack, Index: idx, Rbrack: t.Rbrack}
	default:
		return target
	}
}

func (c *converter) isPointerType(e ast.Expr) bool {
	t := c.info.TypeOf(e)
	if t == nil {
		return false
	}
	_, ok := t.Underlying().(*types.Pointer)
	return ok
}

// isReferenceType reports whether indexing e writes through a shared reference
// (a map, a slice or a pointer to an array), so the indexed value may be bound
// to a temporary without copying the collection.
func (c *converter) isReferenceType(e ast.Expr) bool {
	t := c.info.TypeOf(e)
	if t == nil {
		return false
	}
	switch u := t.Underlying().(type) {
	case *types.Map, *types.Slice:
		return true
	case *types.Pointer:
		_, array := u.Elem().Underlying().(*types.Array)
		return array
	}
	return false
}

// tuplePlainTargets reports whether every target is a bare name, which is the
// shape BashPPAssign carries directly.
func tuplePlainTargets(targets []ast.Expr) bool {
	for _, target := range targets {
		if _, ok := target.(*ast.Ident); !ok {
			return false
		}
	}
	return true
}
