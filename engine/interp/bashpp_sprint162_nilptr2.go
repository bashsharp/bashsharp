package interp

import (
	"go/constant"
	"go/token"
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Sprint 162, lane interp-nilptr-2: typed scalar storage kind.
//
// A typed float declaration (`var f float32 = 3.14159`) stores the converted
// constant's exact text, which for a rational value is `n/d`. The cell must
// read back as the float it declares, whatever the shell text looks like, so
// that unary and binary operators see a Float operand rather than a String.

// bashPPFloatStorage reports whether the cell holds a floating-point scalar:
// either its recorded kind says so, or its declared type's underlying type is
// float32 / float64.
func (r *Runner) bashPPFloatStorage(cell *bashPPCell) bool {
	if cell.scalarKind == constant.Float {
		return true
	}
	if cell.declType == nil {
		return false
	}
	named, ok := r.bashPPUnderlyingType(cell.declType).(*syntax.BashPPNamedType)
	return ok && named.Name != nil && (named.Name.Value == "float32" || named.Name.Value == "float64")
}

// bashPPFloatText reads a float cell's text: a Go float literal, or the exact
// `n/d` rendering of a rational value. The result is Unknown for other text.
func bashPPFloatText(text string) constant.Value {
	if v := constant.MakeFromLiteral(text, token.FLOAT, 0); v.Kind() != constant.Unknown {
		return v
	}
	if exact := bashPPExactFloatText(text); exact != nil {
		return exact
	}
	return constant.MakeUnknown()
}

// bashPPExactFloatText parses the exact `numerator/denominator` rendering that
// bashPPScalarString gives a rational float value. It returns nil for any
// other text.
func bashPPExactFloatText(text string) constant.Value {
	parts := strings.Split(text, "/")
	if len(parts) != 2 {
		return nil
	}
	numerator := constant.MakeFromLiteral(parts[0], token.FLOAT, 0)
	denominator := constant.MakeFromLiteral(parts[1], token.FLOAT, 0)
	if numerator.Kind() == constant.Unknown || denominator.Kind() == constant.Unknown || constant.Sign(denominator) == 0 {
		return nil
	}
	return constant.BinaryOp(numerator, token.QUO, denominator)
}

// bashPPBuiltinScalarKind is the constant kind a builtin scalar type's cells
// store; Unknown for a type that is not a builtin scalar.
func bashPPBuiltinScalarKind(name string) constant.Kind {
	switch {
	case name == "string":
		return constant.String
	case name == "bool":
		return constant.Bool
	case name == "float32" || name == "float64":
		return constant.Float
	case name == "complex64" || name == "complex128":
		return constant.Complex
	case bashPPIntegerType(name):
		return constant.Int
	}
	return constant.Unknown
}

// A nil function value as an argument.
//
// A func-typed parameter accepts nil — the literal, or a func variable that
// holds no closure. Both arrive as an empty argument text: a closure is always
// a handle, and a declared function named as an argument was bound to one
// before the type check (see bashPPBindFuncValueArgs), so the empty text is
// the only spelling of a nil func value. The checker has already verified the
// argument's type, and a call through the bound parameter faults as Go's nil
// function call does.
func (r *Runner) goSourceNilFuncArgument(arg string) bool {
	return r.bashPPGoSource && arg == ""
}

// A promoted method selected through a nil pointer.
//
// Resolving `o.M()` where o is a nil *Outer and M is promoted from an
// embedded field dereferences o on the way to the receiver, and the binding
// step raises Go's nil-dereference panic there (bashPPBindPromotedMethod).
// The lookup then reports no callee, and a call site that read that as an
// undefined name would print a second, wrong diagnostic. goSourceCalleeFaulted
// tells the call sites the failed lookup was that panic: the expression is
// interrupted, not undefined.
func (r *Runner) goSourceCalleeFaulted() bool {
	return r.bashPPGoSource && r.bashPPPanicHalts()
}

// recover() as the value of an assignment.
//
// `r = recover()` and `_ = recover()` assign the recovered value — the
// interface value the expression forms hand out — to an existing variable
// or discard it; the lookup that serves declared functions has nothing for
// the predeclared call. It reports whether the assignment was this shape.
func (r *Runner) goSourceRecoverAssign(assign *syntax.BashPPAssign) bool {
	if !r.bashPPGoSource || len(assign.Names) != 1 || !bashPPRecoverExpr(assign.Call) || r.bashPPFuncs["recover"] != nil || r.bashPPScope.lookup("recover") != nil {
		return false
	}
	cell, err := r.bashPPStructuredArgCell(nil, assign.Call)
	if err != nil || cell == nil {
		return false
	}
	// The source was typechecked: the target is an interface variable, and
	// the value keeps that variable's static type, as a dependency result
	// assigned to one does (goSourceNativeAssignCall).
	if target := r.bashPPScope.lookup(assign.Names[0].Value).view(); target != nil {
		if _, iface := r.bashPPInterfaceType(target.declType); iface {
			cell.declType = target.declType
			cell.typeName = target.typeName
		}
	}
	r.bashPPCommitTupleAssign(assign, []*bashPPCell{cell})
	return true
}

// A named pointer type as a parameter or result.
//
// `type PS *dch` is a pointer type under its own name, and a parameter or
// result declared PS binds a pointer exactly as one declared *dch does. The
// binding sites read the declared text and took a leading `*` as the whole
// test, so a PS value arrived without its pointer flag and was read as an
// empty scalar downstream: a nil where Go has the pointer. The declared name
// is a pointer when it is spelled as one or when its underlying type is one.
func (r *Runner) bashPPDeclaredPointer(declared string) bool {
	if strings.HasPrefix(declared, "*") {
		return true
	}
	if declared == "" {
		return false
	}
	_, ok := r.bashPPPointerType(&syntax.BashPPNamedType{Name: &syntax.Lit{Value: declared}})
	return ok
}

// bashPPClosureType is the signature a closure names as a value: the
// literal's own, with the type arguments of the instantiation that made it
// substituted — a literal written in a generic body is part of that
// instantiation, and the type it is asserted against is written in the
// instantiated frame too.
func bashPPClosureType(fn *bashPPFunc) syntax.BashPPTypeExpr {
	typ := syntax.BashPPTypeExpr(bashPPFuncLitType(fn.lit))
	if len(fn.typeArgs) > 0 {
		typ = bashPPSubstituteType(typ, fn.typeArgs)
	}
	return typ
}
