// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"context"
	"errors"
	"go/constant"
	"strconv"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// bashPPScalarIntFunc is the deliberately narrow, allocation-free execution
// plan for pure int functions. The compiler accepts a whole function or none
// of it: a rejected node leaves execution on the general evaluator before the
// body has performed any work.
//
// This is an interpreter plan, not native execution. It removes repeated AST
// dispatch, lexical maps, shell frames, and boxed cells from scalar recursion
// while retaining the original syntax as the source of the plan.
type bashPPScalarIntFunc struct {
	params  int
	results int
	body    []bashPPScalarIntStmt
}

const (
	bashPPScalarIntMaxSlots   = 16
	bashPPScalarIntMaxResults = 4
)

type bashPPScalarIntStmt struct {
	kind bashPPScalarIntStmtKind
	cond *bashPPScalarIntExpr
	then []bashPPScalarIntStmt
	call []*bashPPScalarIntExpr
	dst  []int
	ret  []*bashPPScalarIntExpr
}

type bashPPScalarIntStmtKind uint8

const (
	bashPPScalarIntIf bashPPScalarIntStmtKind = iota + 1
	bashPPScalarIntCall
	bashPPScalarIntReturn
)

type bashPPScalarIntExpr struct {
	kind        bashPPScalarIntExprKind
	value, slot int
	op          string
	left, right *bashPPScalarIntExpr
}

type bashPPScalarIntExprKind uint8

const (
	bashPPScalarIntLiteral bashPPScalarIntExprKind = iota + 1
	bashPPScalarIntSlot
	bashPPScalarIntBinary
)

type bashPPScalarIntCompiler struct {
	name    string
	params  int
	results int
	slots   map[string]int
	next    int
}

func bashPPCompileScalarIntFunc(fn *bashPPFunc) *bashPPScalarIntFunc {
	if fn == nil || fn.decl == nil || fn.decl.Body == nil || fn.decl.Receiver != nil ||
		len(fn.decl.Decorators) > 0 || len(fn.advised) > 0 || fn.goError ||
		fn.decl.Agentic != nil || len(fn.decl.TypeParams) > 0 {
		return nil
	}
	params := bashppParams(fn.params())
	results := bashppResultTypes(fn.bodyResults())
	if len(params) == 0 || len(results) == 0 || len(results) > bashPPScalarIntMaxResults {
		return nil
	}
	c := &bashPPScalarIntCompiler{name: fn.name(), params: len(params), results: len(results), slots: make(map[string]int, len(params)+4)}
	for _, param := range params {
		if param.name == "" || param.declared != "int" || param.variadic || param.defaultValue != nil {
			return nil
		}
		if _, exists := c.slots[param.name]; exists {
			return nil
		}
		c.slots[param.name] = c.next
		c.next++
	}
	for _, typ := range results {
		if typ != "int" {
			return nil
		}
	}
	body, ok := c.block(fn.decl.Body)
	if !ok || c.next > bashPPScalarIntMaxSlots {
		return nil
	}
	return &bashPPScalarIntFunc{params: len(params), results: len(results), body: body}
}

func (c *bashPPScalarIntCompiler) block(block *syntax.Block) ([]bashPPScalarIntStmt, bool) {
	if block == nil {
		return nil, false
	}
	out := make([]bashPPScalarIntStmt, 0, len(block.Stmts))
	for _, stmt := range block.Stmts {
		if stmt == nil || stmt.Cmd == nil || stmt.Negated || stmt.Background || stmt.Coprocess || stmt.Disown || len(stmt.Redirs) > 0 {
			return nil, false
		}
		switch cmd := stmt.Cmd.(type) {
		case *syntax.BashPPIf:
			if cmd.Init != nil || cmd.InitStmt != nil || cmd.Else != nil || cmd.Cond == nil {
				return nil, false
			}
			cond, ok := c.expr(cmd.Cond)
			if !ok || !bashPPScalarIntBoolExpr(cond) {
				return nil, false
			}
			then, ok := c.block(cmd.Then)
			if !ok {
				return nil, false
			}
			out = append(out, bashPPScalarIntStmt{kind: bashPPScalarIntIf, cond: cond, then: then})
		case *syntax.BashPPShortDecl:
			if cmd.Call == nil || len(cmd.Lhs) == 0 || len(cmd.Call.Fun) != 1 || cmd.Call.Fun[0].Value != c.name ||
				cmd.Call.CalleeExpr != nil || cmd.Call.FuncLit != nil || len(cmd.Call.TypeArgs) > 0 || len(cmd.Call.ArgNames) > 0 {
				return nil, false
			}
			args := make([]*bashPPScalarIntExpr, len(cmd.Call.ArgExprs))
			if len(args) != c.params || len(cmd.Lhs) != c.results || len(cmd.Call.Args) != len(args) {
				return nil, false
			}
			for i, arg := range cmd.Call.ArgExprs {
				var ok bool
				args[i], ok = c.expr(arg)
				if !ok || bashPPScalarIntBoolExpr(args[i]) {
					return nil, false
				}
			}
			dst := make([]int, len(cmd.Lhs))
			for i, lhs := range cmd.Lhs {
				if lhs == nil || lhs.Value == "_" {
					dst[i] = -1
					continue
				}
				if _, exists := c.slots[lhs.Value]; exists || c.next >= bashPPScalarIntMaxSlots {
					return nil, false
				}
				dst[i] = c.next
				c.slots[lhs.Value] = c.next
				c.next++
			}
			out = append(out, bashPPScalarIntStmt{kind: bashPPScalarIntCall, call: args, dst: dst})
		case *syntax.BashPPReturn:
			exprs := cmd.ResultExprs
			if len(exprs) == 0 && cmd.Expr != nil {
				exprs = []syntax.BashPPExpr{cmd.Expr}
			}
			if cmd.Call != nil || cmd.FuncLit != nil || len(exprs) != c.results {
				return nil, false
			}
			ret := make([]*bashPPScalarIntExpr, len(exprs))
			for i, expr := range exprs {
				var ok bool
				ret[i], ok = c.expr(expr)
				if !ok || bashPPScalarIntBoolExpr(ret[i]) {
					return nil, false
				}
			}
			out = append(out, bashPPScalarIntStmt{kind: bashPPScalarIntReturn, ret: ret})
		default:
			return nil, false
		}
	}
	return out, len(out) > 0
}

func (c *bashPPScalarIntCompiler) expr(expr syntax.BashPPExpr) (*bashPPScalarIntExpr, bool) {
	switch x := expr.(type) {
	case *syntax.BashPPParenExpr:
		return c.expr(x.X)
	case *syntax.BashPPBasicLit:
		if x.Kind != "INT" || x.Value == nil {
			return nil, false
		}
		value, err := strconv.ParseInt(x.Value.Value, 0, strconv.IntSize)
		if err != nil {
			return nil, false
		}
		return &bashPPScalarIntExpr{kind: bashPPScalarIntLiteral, value: int(value)}, true
	case *syntax.BashPPIdent:
		if x.Name == nil {
			return nil, false
		}
		slot, ok := c.slots[x.Name.Value]
		if !ok {
			return nil, false
		}
		return &bashPPScalarIntExpr{kind: bashPPScalarIntSlot, slot: slot}, true
	case *syntax.BashPPBinaryExpr:
		if x.Op == nil {
			return nil, false
		}
		switch x.Op.Value {
		case "+", "-", "*", "<", "<=", ">", ">=", "==", "!=":
		default:
			return nil, false
		}
		left, lok := c.expr(x.X)
		right, rok := c.expr(x.Y)
		if !lok || !rok || bashPPScalarIntBoolExpr(left) || bashPPScalarIntBoolExpr(right) {
			return nil, false
		}
		return &bashPPScalarIntExpr{kind: bashPPScalarIntBinary, op: x.Op.Value, left: left, right: right}, true
	}
	return nil, false
}

func bashPPScalarIntBoolExpr(expr *bashPPScalarIntExpr) bool {
	if expr == nil || expr.kind != bashPPScalarIntBinary {
		return false
	}
	switch expr.op {
	case "<", "<=", ">", ">=", "==", "!=":
		return true
	}
	return false
}

type bashPPScalarIntValues struct {
	values [bashPPScalarIntMaxResults]int
	count  int
}

type bashPPScalarIntFrame struct {
	slots [bashPPScalarIntMaxSlots]int
}

type bashPPScalarIntState struct {
	ctx   context.Context
	calls uint32
	stop  bool
}

func (r *Runner) bashPPTryScalarIntInvoke(ctx context.Context, fn *bashPPFunc, args []string, cells []*bashPPCell) ([]string, bool) {
	plan := fn.scalarInt
	if !r.bashPPGoSource || plan == nil || len(args) != plan.params || r.bashPPFuncNest() != 0 ||
		len(r.bashPPCallChannels) > 0 || len(r.bashPPCallInterfaces) > 0 || r.bashPPCallSpread ||
		r.bashPPPanicking() || r.exit.exiting {
		return nil, false
	}
	var frame bashPPScalarIntFrame
	for i, text := range args {
		value, err := strconv.Atoi(text)
		if err != nil {
			return nil, false
		}
		if i < len(cells) && cells[i] != nil {
			scalar := r.bashPPScalarFromCell(cells[i])
			if scalar.value == nil || scalar.value.Kind() != constant.Int || scalar.typ != "int" {
				return nil, false
			}
		}
		frame.slots[i] = value
	}
	state := bashPPScalarIntState{ctx: ctx}
	values, returned := plan.run(&state, frame)
	if state.stop {
		r.exit.fatal(ctx.Err())
		return nil, true
	}
	if !returned || values.count != plan.results {
		// This should be unreachable for an eligible body, but falling back now
		// would execute a body twice. Treat a malformed plan as a hard internal
		// failure instead of risking duplicated observable work.
		r.exit.fatal(errors.New("bash++: scalar int plan completed without its declared results"))
		return nil, true
	}
	results := make([]string, values.count)
	r.bashPPResultCells = make([]*bashPPCell, values.count)
	intType := &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "int"}}
	for i := range values.count {
		text := strconv.Itoa(values.values[i])
		results[i] = text
		r.bashPPResultCells[i] = &bashPPCell{
			vr:         expand.Variable{Set: true, Kind: expand.String, Str: text},
			scalarKind: constant.Int, typeName: "int", declType: intType,
		}
	}
	r.exit.code = 0
	r.exit.returning = false
	return results, true
}

func (p *bashPPScalarIntFunc) run(state *bashPPScalarIntState, frame bashPPScalarIntFrame) (bashPPScalarIntValues, bool) {
	state.calls++
	if state.calls&4095 == 0 && state.ctx.Err() != nil {
		state.stop = true
		return bashPPScalarIntValues{}, false
	}
	return p.runBlock(state, frame, p.body)
}

func (p *bashPPScalarIntFunc) runBlock(state *bashPPScalarIntState, frame bashPPScalarIntFrame, body []bashPPScalarIntStmt) (bashPPScalarIntValues, bool) {
	for _, stmt := range body {
		switch stmt.kind {
		case bashPPScalarIntIf:
			if stmt.cond.bool(&frame) {
				if values, returned := p.runBlock(state, frame, stmt.then); returned || state.stop {
					return values, returned
				}
			}
		case bashPPScalarIntCall:
			var child bashPPScalarIntFrame
			for i, expr := range stmt.call {
				child.slots[i] = expr.integer(&frame)
			}
			values, returned := p.run(state, child)
			if state.stop || !returned || values.count != len(stmt.dst) {
				return bashPPScalarIntValues{}, false
			}
			for i, slot := range stmt.dst {
				if slot >= 0 {
					frame.slots[slot] = values.values[i]
				}
			}
		case bashPPScalarIntReturn:
			values := bashPPScalarIntValues{count: len(stmt.ret)}
			for i, expr := range stmt.ret {
				values.values[i] = expr.integer(&frame)
			}
			return values, true
		}
	}
	return bashPPScalarIntValues{}, false
}

func (e *bashPPScalarIntExpr) integer(frame *bashPPScalarIntFrame) int {
	switch e.kind {
	case bashPPScalarIntLiteral:
		return e.value
	case bashPPScalarIntSlot:
		return frame.slots[e.slot]
	case bashPPScalarIntBinary:
		left, right := e.left.integer(frame), e.right.integer(frame)
		switch e.op {
		case "+":
			return left + right
		case "-":
			return left - right
		case "*":
			return left * right
		}
	}
	return 0
}

func (e *bashPPScalarIntExpr) bool(frame *bashPPScalarIntFrame) bool {
	left, right := e.left.integer(frame), e.right.integer(frame)
	switch e.op {
	case "<":
		return left < right
	case "<=":
		return left <= right
	case ">":
		return left > right
	case ">=":
		return left >= right
	case "==":
		return left == right
	case "!=":
		return left != right
	}
	return false
}
