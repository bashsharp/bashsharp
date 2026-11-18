package interp

// Sprint: #209; Story: #463; Story-ID: a6f104b906d9
//
// Go's predeclared print and println render their operands the way the
// runtime does (runtime/print.go): a float is its shortest 'g' form at the
// operand's own width, a complex the same in parentheses, and a pointer,
// channel, map, func or slice is a machine address — `0x0` when nil, and
// `[len/cap]0xaddr` for a slice. The interpreter's cells carry none of those
// shapes: a float travels as its exact rational text, a nil reference as an
// untyped nil or a transport struct. This file is the one place both the
// direct and the deferred print paths turn an evaluated operand into the
// bytes Go would write.

import (
	"fmt"
	"go/constant"
	"go/token"
	"math"
	"strconv"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// goSourcePrintScalar renders a scalar operand as Go's print would.
func (r *Runner) goSourcePrintScalar(scalar bashPPScalar) string {
	if scalar.hasNonFinite {
		return strconv.FormatFloat(scalar.nonFinite, 'g', -1, 64)
	}
	if scalar.value == nil {
		return ""
	}
	bits := 64
	if scalar.typ != "" {
		if named, ok := r.bashPPUnderlyingTypeName(scalar.typ); ok {
			switch named {
			case "float32", "complex64":
				bits = 32
			}
		}
	}
	switch scalar.value.Kind() {
	case constant.Float:
		// Float64Val's second result reports exactness, which an untyped
		// 0.1 or a computed 3/10 never has; the nearest float64 is the value.
		f, _ := constant.Float64Val(scalar.value)
		if scalar.negativeZero && f == 0 {
			f = math.Copysign(0, -1)
		}
		return strconv.FormatFloat(f, 'g', -1, bits)
	case constant.Complex:
		return strconv.FormatComplex(bashPPComplexNumber(scalar.value), 'g', -1, 2*bits)
	}
	return bashPPScalarString(scalar.value)
}

// goSourcePrintReference renders nil reference operands. Nilness is decided
// by the same comparison `x == nil` uses.
func (r *Runner) goSourcePrintReference(expr syntax.BashPPExpr) (string, error) {
	isNil, err := r.bashPPCompareExpr(expr, token.EQL, &syntax.BashPPIdent{Name: &syntax.Lit{Value: "nil", ValuePos: expr.Pos(), ValueEnd: expr.End()}})
	if err != nil {
		return "", err
	}
	kind := r.goSourcePrintReferenceKind(expr)
	if !isNil {
		return "", fmt.Errorf("gosource: non-nil reference print is not implemented")
	}
	switch {
	case kind == "slice":
		return "[0/0]0x0", nil
	case kind == "interface":
		return "(0x0,0x0)", nil
	default:
		return "0x0", nil
	}
}

// goSourcePrintReferenceKind names the shape of a nil reference operand.
func (r *Runner) goSourcePrintReferenceKind(expr syntax.BashPPExpr) string {
	var typ syntax.BashPPTypeExpr
	switch x := expr.(type) {
	case *syntax.BashPPParenExpr:
		return r.goSourcePrintReferenceKind(x.X)
	case *syntax.BashPPIdent:
		cell := r.bashPPScope.lookup(x.Name.Value).view()
		if cell == nil {
			return ""
		}
		if cell.interfaceValue != nil {
			return "interface"
		}
		if cell.pointer {
			return "pointer"
		}
		if meta := bashPPCellMeta(cell); meta != nil && meta.kind != "" {
			return meta.kind
		}
		typ = cell.declType
	case *syntax.BashPPConvertExpr:
		typ = r.bashPPConvertTarget(x)
	}
	if typ == nil {
		return ""
	}
	if _, iface := r.bashPPInterfaceType(typ); iface {
		return "interface"
	}
	switch t := r.bashPPUnderlyingType(typ).(type) {
	case *syntax.BashPPCollectionType:
		return t.Kind
	case *syntax.BashPPPointerType:
		return "pointer"
	case *syntax.BashPPChanType:
		return "channel"
	case *syntax.BashPPFuncType:
		return "func"
	}
	return ""
}

// goSourcePrintReferenceOperand reports whether a print/println operand names
// a reference value the scalar evaluator cannot hold — a pointer, slice, map,
// channel, func or interface, or a typed nil spelled as a conversion — so it
// is read as a value cell instead of failing as "not a scalar".
func (r *Runner) goSourcePrintReferenceOperand(expr syntax.BashPPExpr) bool {
	if !r.bashPPGoSource || expr == nil {
		return false
	}
	switch x := expr.(type) {
	case *syntax.BashPPParenExpr:
		return r.goSourcePrintReferenceOperand(x.X)
	case *syntax.BashPPAddressExpr, *syntax.BashPPNewExpr:
		return true
	case *syntax.BashPPIdent:
		if x.Name.Value == "nil" || r.bashPPScope == nil {
			return false
		}
		cell := r.bashPPScope.lookup(x.Name.Value).view()
		if cell == nil {
			return false
		}
		if cell.pointer || cell.interfaceValue != nil || cell.channel != nil {
			return true
		}
		if cell.vr.Kind == expand.Object {
			return true
		}
		if _, closure := r.bashPPClosure(cell.vr.Str); closure {
			return true
		}
		return r.goSourceNilableType(cell.declType)
	case *syntax.BashPPConvertExpr:
		if !goSourceNilLiteral(x.X) {
			return false
		}
		target := r.bashPPConvertTarget(x)
		return target != nil && r.goSourceNilableType(target)
	}
	return false
}
