package interp

// GoSource complex values use the existing exact constant carrier. Runtime
// results round each component to the declared Go width before storage.
import (
	"fmt"
	"go/constant"
	"go/token"
	"math"
	"strconv"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func bashPPComplexConstant(z complex128) constant.Value {
	return constant.BinaryOp(constant.MakeFloat64(real(z)), token.ADD, constant.MakeImag(constant.MakeFloat64(imag(z))))
}

func (r *Runner) bashPPComplexShortDecl(d *syntax.BashPPShortDecl) bool {
	if d.Call == nil || len(d.Lhs) != 1 {
		return false
	}
	if result, handled, err := r.goSourceComplexBuiltinCell(d.Call); handled {
		if err != nil {
			r.errf("%v\n", err)
			r.exit = exitStatus{code: 2}
			return true
		}
		r.bashPPDeclareName(d.Lhs[0].Value, result.vr)
		if target := r.bashPPScope.lookup(d.Lhs[0].Value); target != nil {
			target.publish(result)
		}
		return true
	}
	v, handled, err := r.bashPPComplexBuiltin(d.Call)
	if !handled {
		return false
	}
	if err != nil {
		r.errf("%v\n", err)
		r.exit = exitStatus{code: 2}
		return true
	}
	if v.typ == "" {
		switch v.kind() {
		case constant.Complex:
			v.typ = "complex128"
		case constant.Float:
			v.typ = "float64"
		}
	}
	r.bashPPDeclareName(d.Lhs[0].Value, expand.Variable{Set: true, Kind: expand.String, Str: bashPPScalarStorageString(v)})
	cell := r.bashPPScope.lookup(d.Lhs[0].Value)
	cell.scalarKind = v.kind()
	cell.nonFiniteComplex, cell.hasNonFiniteComplex = v.nonFiniteComplex, v.hasNonFiniteComplex
	cell.typeName = v.typ
	return true
}
func bashPPComplexNumber(v constant.Value) complex128 {
	re, _ := constant.Float64Val(constant.Real(v))
	im, _ := constant.Float64Val(constant.Imag(v))
	return complex(re, im)
}

func bashPPScalarComplex128(v bashPPScalar) (complex128, bool) {
	if v.hasNonFiniteComplex {
		return v.nonFiniteComplex, true
	}
	if v.value == nil || (v.value.Kind() != constant.Int && v.value.Kind() != constant.Float && v.value.Kind() != constant.Complex) {
		return 0, false
	}
	return bashPPComplexNumber(v.value), true
}

func bashPPNonFiniteComplexScalar(value complex128, typ string) bashPPScalar {
	scalar := bashPPScalar{typ: typ, runtime: true, nonFiniteComplex: value, hasNonFiniteComplex: true}
	// Finite values still satisfy the exact-carrier contract used by scalar
	// consumers. The IEEE carrier remains authoritative for signed zero.
	if !math.IsNaN(real(value)) && !math.IsNaN(imag(value)) && !math.IsInf(real(value), 0) && !math.IsInf(imag(value), 0) {
		scalar.value = bashPPComplexConstant(value)
	}
	return scalar
}

func bashPPNonFiniteComplexText(text string) (complex128, bool) {
	value, err := strconv.ParseComplex(text, 128)
	if err != nil {
		return 0, false
	}
	return value, math.IsInf(real(value), 0) || math.IsInf(imag(value), 0) || math.IsNaN(real(value)) || math.IsNaN(imag(value))
}

// Go's runtime operations round intermediate floating components too. Using
// constant.BinaryOp for runtime multiplication would incorrectly keep exact
// products until the final addition and can change cancellation results.
func (r *Runner) bashPPComplexRuntimeOp(op token.Token, left, right bashPPScalar, typ string) (bashPPScalar, bool, error) {
	if !r.bashPPGoSource || !(left.runtime || right.runtime) || (left.kind() != constant.Complex && right.kind() != constant.Complex) {
		return bashPPScalar{}, false, nil
	}
	if op != token.ADD && op != token.SUB && op != token.MUL && op != token.QUO {
		return bashPPScalar{}, false, nil
	}
	base := typ
	if named, ok := r.bashPPUnderlyingTypeName(typ); ok {
		base = named
	}
	a, aok := bashPPScalarComplex128(left)
	b, bok := bashPPScalarComplex128(right)
	if !aok || !bok {
		return bashPPScalar{}, true, fmt.Errorf("BASHPP-EEXPR-OPERAND: complex operation requires numeric operands")
	}
	var z complex128
	if base == "complex64" {
		a, b := complex64(a), complex64(b)
		switch op {
		case token.ADD:
			z = complex128(a + b)
		case token.SUB:
			z = complex128(a - b)
		case token.MUL:
			z = complex128(a * b)
		case token.QUO:
			z = complex128(a / b)
		}
	} else {
		switch op {
		case token.ADD:
			z = a + b
		case token.SUB:
			z = a - b
		case token.MUL:
			z = a * b
		case token.QUO:
			z = a / b
		}
	}
	// Runtime complex values need an IEEE carrier even when finite: a
	// go/constant complex cannot retain the sign bit of either zero component.
	return bashPPNonFiniteComplexScalar(z, typ), true, nil
}
func bashPPParseComplex(text string) constant.Value {
	z, err := strconv.ParseComplex(text, 128)
	if err != nil {
		return constant.MakeUnknown()
	}
	return bashPPComplexConstant(z)
}
func (r *Runner) bashPPConvertComplex(typ string, x bashPPScalar) (bashPPScalar, error) {
	if !r.bashPPGoSource {
		return bashPPScalar{}, fmt.Errorf("BASHPP-ECOMPLEX-UNSUPPORTED: complex values require Go source")
	}
	if x.hasNonFiniteComplex {
		value := x.nonFiniteComplex
		if typ == "complex64" {
			value = complex128(complex64(value))
		}
		return bashPPNonFiniteComplexScalar(value, typ), nil
	}
	if x.value.Kind() != constant.Int && x.value.Kind() != constant.Float && x.value.Kind() != constant.Complex {
		return bashPPScalar{}, fmt.Errorf("cannot convert %s to %s", x.value.Kind(), typ)
	}
	z := bashPPComplexNumber(x.value)
	if typ == "complex64" {
		re, _ := constant.Float32Val(constant.Real(x.value))
		im, _ := constant.Float32Val(constant.Imag(x.value))
		z = complex128(complex(re, im))
	}
	if x.runtime || math.IsInf(real(z), 0) || math.IsInf(imag(z), 0) || math.IsNaN(real(z)) || math.IsNaN(imag(z)) {
		return bashPPNonFiniteComplexScalar(z, typ), nil
	}
	return bashPPScalar{value: bashPPComplexConstant(z), typ: typ, runtime: x.runtime}, nil
}
func (r *Runner) bashPPComplexBuiltin(call *syntax.BashPPCall) (bashPPScalar, bool, error) {
	if !r.bashPPGoSource || len(call.Fun) != 1 {
		return bashPPScalar{}, false, nil
	}
	name := call.Fun[0].Value
	if name != "complex" && name != "real" && name != "imag" {
		return bashPPScalar{}, false, nil
	}
	if _, ok := r.bashPPLookupFunc(call); ok {
		return bashPPScalar{}, false, nil
	}
	count := 1
	if name == "complex" {
		count = 2
	}
	if len(call.ArgExprs) != count {
		return bashPPScalar{}, true, fmt.Errorf("%s requires %d arguments", name, count)
	}
	args := make([]bashPPScalar, count)
	for i, expr := range call.ArgExprs {
		v, err := r.bashPPEvalScalarExpr(expr)
		if err != nil {
			return bashPPScalar{}, true, err
		}
		args[i] = v
	}
	result, err := r.bashPPComplexBuiltinValues(name, args)
	return result, true, err
}

// bashPPComplexBuiltinValues implements the typed rules shared by the scalar
// spelling and Go source's value path. The latter may expand one multi-result
// call into complex's two operands before it reaches here.
func (r *Runner) bashPPComplexBuiltinValues(name string, args []bashPPScalar) (bashPPScalar, error) {
	if name == "complex" {
		for _, a := range args {
			if a.value.Kind() != constant.Int && a.value.Kind() != constant.Float {
				return bashPPScalar{}, fmt.Errorf("complex requires floating-point arguments")
			}
		}
		typ := ""
		runtime := args[0].runtime || args[1].runtime
		for _, a := range args {
			if a.typ != "" {
				underlying, ok := r.bashPPUnderlyingTypeName(a.typ)
				if !ok || (underlying != "float32" && underlying != "float64") {
					return bashPPScalar{}, fmt.Errorf("complex requires floating-point arguments")
				}
				if underlying == "float32" {
					typ = "complex64"
				} else {
					typ = "complex128"
				}
			}
		}
		if runtime {
			re, reOK := bashPPScalarFloat64(args[0])
			im, imOK := bashPPScalarFloat64(args[1])
			if !reOK || !imOK {
				return bashPPScalar{}, fmt.Errorf("complex requires floating-point arguments")
			}
			value := complex(re, im)
			if typ == "complex64" {
				value = complex128(complex64(value))
			}
			return bashPPNonFiniteComplexScalar(value, typ), nil
		}
		v := constant.BinaryOp(args[0].value, token.ADD, constant.MakeImag(args[1].value))
		result, err := r.bashPPTypedScalarResult(v, typ, runtime)
		return result, err
	}
	a := args[0]
	// The spec treats every untyped numeric constant as untyped complex for
	// real and imag. A typed non-complex operand remains invalid.
	if !a.hasNonFiniteComplex && (a.value.Kind() != constant.Complex &&
		(a.runtime || a.value.Kind() != constant.Int && a.value.Kind() != constant.Float)) {
		return bashPPScalar{}, fmt.Errorf("%s requires complex argument", name)
	}
	typ := ""
	if a.typ != "" {
		underlying, ok := r.bashPPUnderlyingTypeName(a.typ)
		if !ok {
			return bashPPScalar{}, fmt.Errorf("%s requires complex argument", name)
		}
		switch underlying {
		case "complex64":
			typ = "float32"
		case "complex128":
			typ = "float64"
		default:
			return bashPPScalar{}, fmt.Errorf("%s requires complex argument", name)
		}
	}
	if a.hasNonFiniteComplex {
		part := real(a.nonFiniteComplex)
		if name == "imag" {
			part = imag(a.nonFiniteComplex)
		}
		if math.IsInf(part, 0) || math.IsNaN(part) {
			return bashPPNonFiniteScalar(part, typ), nil
		}
		return bashPPScalar{value: constant.MakeFloat64(part), typ: typ, runtime: true, negativeZero: part == 0 && math.Signbit(part)}, nil
	}
	v := constant.Real(a.value)
	if name == "imag" {
		v = constant.Imag(a.value)
	}
	return bashPPScalar{value: constant.ToFloat(v), typ: typ, runtime: a.runtime}, nil
}
