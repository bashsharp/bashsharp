// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Type parameters inside a generic BODY.
//
// The parser marks a type parameter USE with [syntax.BashPPTypeParamType] only
// where it can see the parameter list next to it: a signature, a receiver, a
// generic type declaration's own fields. A statement written inside the body —
// `var zero T`, `xs := []T{}`, `new(T)` — is parsed by the ordinary declaration
// and expression recognizers, which have no parameter list in hand, so `T`
// arrives as an ordinary [syntax.BashPPNamedType]. Signature substitution alone
// therefore left the body referring to a type nobody declared, and the runner
// reported `undefined type: T`.
//
// The repair binds the frame's type arguments on the runner and substitutes
// them into each statement as it is dispatched. Substitution is on a CLONE:
// the syntax node is shared by every instantiation of the function (and by any
// other runner reading the same AST), so rewriting it in place would make the
// first call's type arguments permanent.
//
// The bindings are frame-scoped, not stacked: [Runner.bashPPEnterFrame]
// replaces them wholesale with the callee's own arguments — nil for an
// ordinary function — and [bashPPFrame.leave] restores the caller's. A callee
// therefore never sees its caller's `T`, which is Go's rule and is what keeps
// two instantiations of the same method independent.

// bashPPBindTypeExpr resolves the type parameters bound by the current frame.
func (r *Runner) bashPPBindTypeExpr(typ syntax.BashPPTypeExpr) syntax.BashPPTypeExpr {
	if typ == nil || len(r.bashPPTypeParamArgs) == 0 {
		return typ
	}
	return bashPPSubstituteType(typ, r.bashPPTypeParamArgs)
}

// bashPPBindTypeLit rewrites the text spelling that travels beside a type
// expression. DeclType and ConvType are literals rather than type nodes, and
// several paths — assignability messages, the scalar carrier's kind lookup —
// read the text rather than the tree, so leaving it saying `T` would resolve
// the declaration correctly and then describe it wrongly.
func (r *Runner) bashPPBindTypeLit(lit *syntax.Lit, bound syntax.BashPPTypeExpr) *syntax.Lit {
	if lit == nil || bound == nil {
		return lit
	}
	text := bashPPTypeText(bound)
	if text == "" || text == lit.Value {
		return lit
	}
	cp := *lit
	cp.Value = text
	return &cp
}

// bashPPBindDecl substitutes the frame's type arguments into a var/const
// declaration. Nested collections come along for free: substitution walks the
// type tree, so `[]T`, `map[string][]T` and `Box[T]` are all rewritten to the
// same depth as the bare name.
func (r *Runner) bashPPBindDecl(d *syntax.BashPPDecl) *syntax.BashPPDecl {
	if d == nil || len(r.bashPPTypeParamArgs) == 0 {
		return d
	}
	// A type DECLARATION introduces a name of its own; its right-hand side may
	// legitimately mention the enclosing parameter, but its own parameter list
	// is bound at instantiation rather than here.
	bound := r.bashPPBindTypeExpr(d.DeclTypeExpr)
	init := r.bashPPBindExprs(d.InitExpr)
	if bound == d.DeclTypeExpr && init == d.InitExpr {
		return d
	}
	cp := *d
	cp.DeclTypeExpr = bound
	cp.InitExpr = init
	if bound != d.DeclTypeExpr {
		cp.DeclType = r.bashPPBindTypeLit(d.DeclType, bound)
		cp.StructFields = bashPPSubstituteFields(d.StructFields, r.bashPPTypeParamArgs)
	}
	return &cp
}

// bashPPBindConstGroup substitutes into every spec of a parenthesized const
// group, which carries its own per-spec declared types.
func (r *Runner) bashPPBindConstGroup(g *syntax.BashPPConstGroup) *syntax.BashPPConstGroup {
	if g == nil || len(r.bashPPTypeParamArgs) == 0 {
		return g
	}
	changed := false
	specs := make([]*syntax.BashPPConstSpec, len(g.Specs))
	for i, spec := range g.Specs {
		specs[i] = spec
		bound := r.bashPPBindTypeExpr(spec.DeclTypeExpr)
		if bound == spec.DeclTypeExpr {
			continue
		}
		sc := *spec
		sc.DeclTypeExpr = bound
		sc.DeclType = r.bashPPBindTypeLit(spec.DeclType, bound)
		specs[i] = &sc
		changed = true
	}
	if !changed {
		return g
	}
	cp := *g
	cp.Specs = specs
	return &cp
}

func (r *Runner) bashPPBindShortDecl(d *syntax.BashPPShortDecl) *syntax.BashPPShortDecl {
	if d == nil || len(r.bashPPTypeParamArgs) == 0 {
		return d
	}
	expr := r.bashPPBindExprs(d.Expr)
	call := r.bashPPBindCallNode(d.Call)
	// `make(chan T, n)` carries its element both as a type tree and as the
	// bare literal the channel records; substitution rewrites the pair.
	var makeChan *syntax.BashPPMakeChan
	if d.MakeChan != nil && d.MakeChan.ChanType != nil {
		if bound, ok := r.bashPPBindTypeExpr(d.MakeChan.ChanType).(*syntax.BashPPChanType); ok && bound != d.MakeChan.ChanType {
			mc := *d.MakeChan
			mc.ChanType = bound
			makeChan = &mc
		}
	}
	if expr == d.Expr && call == d.Call && makeChan == nil {
		return d
	}
	cp := *d
	cp.Expr = expr
	cp.Call = call
	if makeChan != nil {
		cp.MakeChan = makeChan
	}
	return &cp
}

func (r *Runner) bashPPBindAssign(a *syntax.BashPPAssign) *syntax.BashPPAssign {
	if a == nil || len(r.bashPPTypeParamArgs) == 0 {
		return a
	}
	target := r.bashPPBindExprs(a.TargetExpr)
	value := r.bashPPBindExprs(a.ValueExpr)
	call := r.bashPPBindCallNode(a.Call)
	values, valuesChanged := r.bashPPBindExprList(a.ValueExprs)
	if target == a.TargetExpr && value == a.ValueExpr && call == a.Call && !valuesChanged {
		return a
	}
	cp := *a
	cp.TargetExpr, cp.ValueExpr, cp.Call, cp.ValueExprs = target, value, call, values
	return &cp
}

func (r *Runner) bashPPBindIncDec(s *syntax.BashPPIncDec) *syntax.BashPPIncDec {
	if s == nil || len(r.bashPPTypeParamArgs) == 0 {
		return s
	}
	target := r.bashPPBindExprs(s.Target)
	if target == s.Target {
		return s
	}
	cp := *s
	cp.Target = target
	return &cp
}

func (r *Runner) bashPPBindUpdate(s *syntax.BashPPUpdate) *syntax.BashPPUpdate {
	if s == nil || len(r.bashPPTypeParamArgs) == 0 {
		return s
	}
	target, value := r.bashPPBindExprs(s.Target), r.bashPPBindExprs(s.Value)
	if target == s.Target && value == s.Value {
		return s
	}
	cp := *s
	cp.Target, cp.Value = target, value
	return &cp
}

func (r *Runner) bashPPBindReturn(s *syntax.BashPPReturn) *syntax.BashPPReturn {
	if s == nil || len(r.bashPPTypeParamArgs) == 0 {
		return s
	}
	expr := r.bashPPBindExprs(s.Expr)
	call := r.bashPPBindCallNode(s.Call)
	if expr == s.Expr && call == s.Call {
		return s
	}
	cp := *s
	cp.Expr, cp.Call = expr, call
	return &cp
}

func (r *Runner) bashPPBindSwitch(s *syntax.BashPPSwitch) *syntax.BashPPSwitch {
	if s == nil || len(r.bashPPTypeParamArgs) == 0 {
		return s
	}
	tag := r.bashPPBindExprs(s.Tag)
	arms := make([]*syntax.BashPPSwitchArm, len(s.Arms))
	changed := tag != s.Tag
	for i, arm := range s.Arms {
		arms[i] = arm
		exprs, armChanged := r.bashPPBindExprList(arm.Exprs)
		types := make([]syntax.BashPPTypeExpr, len(arm.Types))
		typesChanged := false
		for j, typ := range arm.Types {
			types[j] = r.bashPPBindTypeExpr(typ)
			typesChanged = typesChanged || types[j] != typ
		}
		if !armChanged && !typesChanged {
			continue
		}
		ac := *arm
		ac.Exprs, ac.Types = exprs, types
		arms[i] = &ac
		changed = true
	}
	if !changed {
		return s
	}
	cp := *s
	cp.Tag, cp.Arms = tag, arms
	return &cp
}

// bashPPBindRange substitutes the operand of a range written in a generic
// body. The parser cannot mark body-local uses such as []V or map[K]V with the
// enclosing function's parameters, so leaving the operand shared would make
// iteration cells retain the abstract V/K identities after instantiation.
func (r *Runner) bashPPBindRange(s *syntax.BashPPRange) *syntax.BashPPRange {
	if s == nil || len(r.bashPPTypeParamArgs) == 0 {
		return s
	}
	expr := r.bashPPBindExprs(s.Expr)
	call := r.bashPPBindCallNode(s.Call)
	if expr == s.Expr && call == s.Call {
		return s
	}
	cp := *s
	cp.Expr, cp.Call = expr, call
	return &cp
}

func (r *Runner) bashPPBindExprList(exprs []syntax.BashPPExpr) ([]syntax.BashPPExpr, bool) {
	if len(exprs) == 0 {
		return exprs, false
	}
	out := make([]syntax.BashPPExpr, len(exprs))
	changed := false
	for i, expr := range exprs {
		out[i] = r.bashPPBindExprs(expr)
		if out[i] != expr {
			changed = true
		}
	}
	if !changed {
		return exprs, false
	}
	return out, true
}

func (r *Runner) bashPPBindCallNode(c *syntax.BashPPCall) *syntax.BashPPCall {
	if c == nil || len(r.bashPPTypeParamArgs) == 0 {
		return c
	}
	argType := r.bashPPBindTypeExpr(c.ArgType)
	args, argsChanged := r.bashPPBindExprList(c.ArgExprs)
	// A computed callee — `reflect.TypeOf(new(T)).String()` — is an
	// expression of the body like any other, and the parameter it mentions
	// is bound the same way.
	callee := r.bashPPBindExprs(c.CalleeExpr)
	typeArgs := c.TypeArgs
	typeArgsChanged := false
	if len(c.TypeArgs) > 0 {
		typeArgs = make([]*syntax.BashPPTypeArg, len(c.TypeArgs))
		for i, arg := range c.TypeArgs {
			typeArgs[i] = arg
			bound := r.bashPPBindTypeExpr(arg.ArgType)
			if bound == arg.ArgType {
				continue
			}
			ac := *arg
			ac.ArgType = bound
			typeArgs[i] = &ac
			typeArgsChanged = true
		}
		if !typeArgsChanged {
			typeArgs = c.TypeArgs
		}
	}
	if argType == c.ArgType && !argsChanged && !typeArgsChanged && callee == c.CalleeExpr {
		return c
	}
	cp := *c
	cp.ArgType, cp.ArgExprs, cp.TypeArgs, cp.CalleeExpr = argType, args, typeArgs, callee
	return &cp
}

// bashPPBindExprs substitutes the frame's type arguments everywhere a type can
// appear inside an expression: composite literal types, `new(T)`, `make([]T,
// n)`, type assertions, scalar conversions, and explicit instantiation
// arguments on a nested call.
func (r *Runner) bashPPBindExprs(x syntax.BashPPExpr) syntax.BashPPExpr {
	if x == nil || len(r.bashPPTypeParamArgs) == 0 {
		return x
	}
	switch e := x.(type) {
	case *syntax.BashPPCompositeLit:
		litType := r.bashPPBindTypeExpr(e.LitType)
		elems := make([]*syntax.BashPPCompositeElem, len(e.Elems))
		changed := litType != e.LitType
		for i, elem := range e.Elems {
			elems[i] = elem
			key, value := r.bashPPBindExprs(elem.Key), r.bashPPBindExprs(elem.Value)
			if key == elem.Key && value == elem.Value {
				continue
			}
			ec := *elem
			ec.Key, ec.Value = key, value
			elems[i] = &ec
			changed = true
		}
		if !changed {
			return x
		}
		cp := *e
		cp.LitType, cp.Elems = litType, elems
		return &cp
	case *syntax.BashPPNewExpr:
		alloc := r.bashPPBindTypeExpr(e.AllocType)
		init := r.bashPPBindExprs(e.Init)
		if alloc == e.AllocType && init == e.Init {
			return x
		}
		cp := *e
		cp.AllocType, cp.Init = alloc, init
		return &cp
	case *syntax.BashPPTypeAssertExpr:
		assert := r.bashPPBindTypeExpr(e.Assert)
		inner := r.bashPPBindExprs(e.X)
		if assert == e.Assert && inner == e.X {
			return x
		}
		cp := *e
		cp.Assert, cp.X = assert, inner
		return &cp
	case *syntax.BashPPConvertExpr:
		// A conversion names its target with a bare literal, so the binding is
		// applied to the NAME rather than to a type tree. Only a parameter
		// bound to something the conversion surface can spell is rewritten; a
		// composite target is left alone for the conversion checker to reject
		// with its own diagnostic rather than a mangled one.
		bound := r.bashPPTypeParamArgs[e.ConvType.Value]
		inner := r.bashPPBindExprs(e.X)
		convType, convTypeExpr := e.ConvType, e.ConvTypeExpr
		if bound != nil {
			convType = r.bashPPBindTypeLit(e.ConvType, bound)
			if convTypeExpr != nil {
				convTypeExpr = bound
			}
		} else if e.ConvTypeExpr != nil {
			// A target that only MENTIONS a parameter — `IteratorFunc[R]`,
			// `*T`, `[]T` — is rewritten through its type tree, and the
			// literal spelling follows it.
			if rebound := r.bashPPBindTypeExpr(e.ConvTypeExpr); rebound != e.ConvTypeExpr {
				convTypeExpr = rebound
				convType = r.bashPPBindTypeLit(e.ConvType, rebound)
			}
		}
		if inner == e.X && convType == e.ConvType && convTypeExpr == e.ConvTypeExpr {
			return x
		}
		cp := *e
		cp.ConvType, cp.ConvTypeExpr, cp.X = convType, convTypeExpr, inner
		return &cp
	case *syntax.BashPPCall:
		if bound := r.bashPPBindCallNode(e); bound != e {
			return bound
		}
		return x
	case *syntax.BashPPParenExpr:
		inner := r.bashPPBindExprs(e.X)
		if inner == e.X {
			return x
		}
		cp := *e
		cp.X = inner
		return &cp
	case *syntax.BashPPUnaryExpr:
		inner := r.bashPPBindExprs(e.X)
		if inner == e.X {
			return x
		}
		cp := *e
		cp.X = inner
		return &cp
	case *syntax.BashPPAddressExpr:
		inner := r.bashPPBindExprs(e.X)
		if inner == e.X {
			return x
		}
		cp := *e
		cp.X = inner
		return &cp
	case *syntax.BashPPDerefExpr:
		inner := r.bashPPBindExprs(e.X)
		if inner == e.X {
			return x
		}
		cp := *e
		cp.X = inner
		return &cp
	case *syntax.BashPPBinaryExpr:
		left, right := r.bashPPBindExprs(e.X), r.bashPPBindExprs(e.Y)
		if left == e.X && right == e.Y {
			return x
		}
		cp := *e
		cp.X, cp.Y = left, right
		return &cp
	case *syntax.BashPPIndexExpr:
		inner, index := r.bashPPBindExprs(e.X), r.bashPPBindExprs(e.Index)
		if inner == e.X && index == e.Index {
			return x
		}
		cp := *e
		cp.X, cp.Index = inner, index
		return &cp
	case *syntax.BashPPSliceExpr:
		inner := r.bashPPBindExprs(e.X)
		low, high, max := r.bashPPBindExprs(e.Low), r.bashPPBindExprs(e.High), r.bashPPBindExprs(e.Max)
		if inner == e.X && low == e.Low && high == e.High && max == e.Max {
			return x
		}
		cp := *e
		cp.X, cp.Low, cp.High, cp.Max = inner, low, high, max
		return &cp
	case *syntax.BashPPSelectorExpr:
		inner := r.bashPPBindExprs(e.X)
		if inner == e.X {
			return x
		}
		cp := *e
		cp.X = inner
		return &cp
	}
	return x
}

// bashPPScalarNamedType turns the type name a scalar carries into the type
// it names. A conversion to an instantiated generic type spells its result
// with the type arguments — `IteratorFunc[int]` — and those arguments are
// what the receiver's methods and the interface checks are bound with, so
// the spelling is parsed back into a tree and the bare name returned beside
// it for the declaration lookup. Any other name is the named type it is.
func bashPPScalarNamedType(name string) (syntax.BashPPTypeExpr, string) {
	if strings.Contains(name, "[") {
		if named, ok := syntax.BashPPTypeExprFromText(name).(*syntax.BashPPNamedType); ok && named.Name != nil && len(named.TypeArgs) > 0 {
			return named, named.Name.Value
		}
	}
	return &syntax.BashPPNamedType{Name: &syntax.Lit{Value: name}}, name
}
