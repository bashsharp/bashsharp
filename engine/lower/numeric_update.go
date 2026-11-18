package lower

import (
	"go/token"
	"go/types"
	"strconv"
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// numericUpdate emits one bounded compound assignment as a checked runtime
// call rather than as Go's own `target op= value`.
//
// The plain Go form cannot carry this statement: a zero divisor panics, a
// shift by a negative count panics, and an operand the target type cannot
// carry is either a static type error or a silent truncation. Each of those is
// a positioned diagnostic in the source language, so the operation is handed
// to the runtime's NumericUpdate, which decides and reports instead of
// trapping.
//
// Evaluation order is Go's own: the target address is the call's first operand
// and the right-hand side its second, and the spec orders the calls inside
// operands left to right. The target is therefore evaluated before the RHS and
// each exactly once, which also means that whatever effects those operands had
// remain after a rejected update — only the target's value is left untouched.
//
// The failure is reported through [emitter.operationFailure], which prints the
// diagnostic and records the status on the program. Whether the program
// continues past it stays with the caller, exactly as it does in the engine.
func (e *emitter) numericUpdate(n *syntax.BashPPUpdate, target, rhs, targetType string) (string, error) {
	if n == nil {
		return "", e.fail(nil, CodeExpr, "compound assignment is absent")
	}
	if n.Op == nil || n.Target == nil || n.Value == nil {
		return "", e.fail(numericUpdatePosition(n), CodeExpr, "compound assignment lacks typed expressions")
	}
	if target == "" || rhs == "" {
		return "", e.fail(numericUpdatePosition(n), CodeExpr, "compound assignment lacks a lowered target or value")
	}
	if !numericUpdateOperator(n.Op.Value) {
		return "", e.fail(numericUpdatePosition(n), CodeUnsupported, "compound assignment operator "+n.Op.Value)
	}
	// The engine reports an operator failure at the operator, not at the
	// statement, so the emitted site is taken from the same token.
	site := e.checkedValueSite(n.Op, numericUpdateName(n.Target))
	failure := e.prefix + "updateFailure"
	call := e.prefix + "rt.NumericUpdate(&(" + target + "), " + e.numericUpdateValue(n, rhs, targetType) + ", " + strconv.Quote(n.Op.Value) + ", " + site + ")"
	return "if " + failure + " := " + call + "; " + failure + " != nil {" + e.operationFailure(failure) + "}", nil
}

// numericUpdateValue gives an untyped source constant the target's own type.
//
// Passed on its own the constant would be inferred at Go's default type — int
// for `0`, float64 for `0.5` — and a `float32` or named-width target would
// then be updated through a type the source never named. A constant the target
// cannot carry remains unconverted so the checked operation reports the source
// overflow diagnostic without rejecting the generated Go program.
//
// A shift count is deliberately excluded: it is not an operand of the target's
// width, so converting it would reject `wide <<= 300`, which is a defined
// shift to zero rather than an overflowing operand.
func (e *emitter) numericUpdateValue(n *syntax.BashPPUpdate, rhs, targetType string) string {
	if targetType == "" || !numericUpdateTypeName(targetType) {
		return rhs
	}
	if op := n.Op.Value; op == "<<=" || op == ">>=" {
		return rhs
	}
	if !numericUpdateUntypedConstant(n.Value) {
		return rhs
	}
	conversion := targetType + "(" + rhs + ")"
	checkedType := targetType
	if decl := e.declaredTypes[targetType]; decl != nil {
		if decl.DeclType != nil {
			checkedType = decl.DeclType.Value
		} else if decl.DeclTypeExpr != nil {
			if typ, err := e.typeExpr(decl.DeclTypeExpr); err == nil {
				checkedType = typ
			}
		}
	}
	if scalarType(checkedType) {
		if _, err := types.Eval(token.NewFileSet(), nil, token.NoPos, checkedType+"("+rhs+")"); err != nil {
			return rhs
		}
	}
	return conversion
}

// numericUpdatePosition is the node a diagnostic is anchored to. An update
// takes its own position from the source words around it, so a statement that
// reaches this seam without them is reported at the nearest node that has one
// rather than being dereferenced for a position it does not have.
func numericUpdatePosition(n *syntax.BashPPUpdate) syntax.Node {
	switch {
	case n == nil:
		return nil
	case n.TargetWord != nil:
		return n
	case n.Op != nil:
		return n.Op
	case n.Target != nil:
		return n.Target
	}
	return nil
}

func numericUpdateOperator(op string) bool {
	switch op {
	case "+=", "-=", "*=", "/=", "%=", "&=", "|=", "^=", "<<=", ">>=", "&^=":
		return true
	}
	return false
}

// numericUpdateTypeName reports whether the lowered target type is a plain
// type name, which is the only spelling that is also a conversion. A composite
// or pointer spelling is not numeric and is not this helper's statement.
func numericUpdateTypeName(typ string) bool {
	if typ == "" {
		return false
	}
	for i, r := range typ {
		switch {
		case r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z':
		case r == '.' && i > 0:
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return !strings.HasSuffix(typ, ".")
}

// numericUpdateUntypedConstant reports whether the source right-hand side is an
// untyped numeric constant expression. An explicit conversion, an identifier
// and a call all carry their own type already and are passed through unchanged.
func numericUpdateUntypedConstant(x syntax.BashPPExpr) bool {
	switch x := x.(type) {
	case *syntax.BashPPBasicLit:
		return x.Kind == "INT" || x.Kind == "FLOAT" || x.Kind == "CHAR"
	case *syntax.BashPPParenExpr:
		return numericUpdateUntypedConstant(x.X)
	case *syntax.BashPPUnaryExpr:
		switch x.Op.Value {
		case "-", "+", "^":
			return numericUpdateUntypedConstant(x.X)
		}
	case *syntax.BashPPBinaryExpr:
		return numericUpdateUntypedConstant(x.X) && numericUpdateUntypedConstant(x.Y)
	}
	return false
}

// numericUpdateName is the source spelling recorded on the site. It stays a
// spelling: a projection of the target's runtime value would make the
// diagnostic depend on state this seam has not read yet.
func numericUpdateName(x syntax.BashPPExpr) string {
	switch x := x.(type) {
	case *syntax.BashPPIdent:
		return x.Name.Value
	case *syntax.BashPPParenExpr:
		return numericUpdateName(x.X)
	case *syntax.BashPPDerefExpr:
		if inner := numericUpdateName(x.X); inner != "" {
			return "*" + inner
		}
	case *syntax.BashPPIndexExpr:
		return numericUpdateName(x.X)
	case *syntax.BashPPSelectorExpr:
		if inner := numericUpdateName(x.X); inner != "" {
			return inner + "." + x.Sel.Value
		}
		return x.Sel.Value
	}
	return ""
}

// Ordinary safe integer updates stay native and keep pure typed units free of
// support imports. Only an operation with a source runtime failure boundary
// needs the checked helper.
func (e *emitter) numericUpdateNeedsCheck(n *syntax.BashPPUpdate, targetType, rhs string) bool {
	if e.goSource {
		// Go's compound assignment already supplies its own type checking and
		// panic semantics; Classic's status-reporting operation is not a Go ABI.
		return false
	}
	if targetType == "string" {
		return false
	}
	if e.projectionExpr(n.Target).kind == projectFloat {
		return true
	}
	switch n.Op.Value {
	case "/=", "%=", "<<=", ">>=":
		return true
	}
	if sourceType := e.projectionExpr(n.Value).sourceType; targetType != "" && sourceType != "" {
		return targetType != sourceType
	}
	if numericUpdateUntypedConstant(n.Value) && targetType != "" {
		check := targetType
		if decl := e.declaredTypes[check]; decl != nil {
			if decl.DeclType != nil {
				check = decl.DeclType.Value
			} else if decl.DeclTypeExpr != nil {
				if typ, err := e.typeExpr(decl.DeclTypeExpr); err == nil {
					check = typ
				}
			}
		}
		if scalarType(check) {
			_, err := types.Eval(token.NewFileSet(), nil, token.NoPos, check+"("+rhs+")")
			return err != nil
		}
	}
	return false
}
