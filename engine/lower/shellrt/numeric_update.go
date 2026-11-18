package shellrt

import (
	"fmt"
	"math"
	"reflect"
	"strings"
)

// Numeric admits every native arithmetic width and any named type defined over
// one. The tilde is what keeps a lowered update in the target's own declared
// type: a `type Counter uint8` update wraps at eight bits and stays a Counter
// rather than being widened to int or float64 on its way through this package.
type Numeric interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64 |
		~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uintptr |
		~float32 | ~float64
}

// NumericError is the positioned diagnostic of a rejected compound assignment.
//
// Kind is the update's own classification, rendered as BASHPP-EUPDATE-<Kind>,
// and Message carries the engine's inner wording — which, for an operator
// rejection, already begins with its own BASHPP-EEXPR-* or BASHPP-EUPDATE-*
// code. That nesting is not an accident of this type: it is the exact text the
// interpreter writes for the same source, and a lowered program is only useful
// as a drop-in if its diagnostics are byte-identical to the engine's.
type NumericError struct {
	Kind, Message string
	Site          ValueSite
}

func (e *NumericError) Code() string { return "BASHPP-EUPDATE-" + e.Kind }
func (e *NumericError) Error() string {
	prefix := ""
	if e.Site.File != "" {
		prefix = fmt.Sprintf("%s: line %d: ", e.Site.File, e.Site.Line)
	}
	return prefix + e.Code() + ": " + e.Message
}
func (*NumericError) ExitStatus() int { return 2 }

// Unwrap exposes the generic positioned diagnostic of this package so that an
// existing [ValueError] consumer — [AsValueError], a recover boundary, a
// reporter that switches on Code — classifies an update failure without an
// edit there.
func (e *NumericError) Unwrap() error {
	return &ValueError{Code: e.Code(), Message: e.Message, Site: e.Site}
}

// numericOperators is the compound assignment set the engine accepts. It is
// spelled with the trailing `=` so a caller passes the source operator through
// unchanged instead of pre-trimming it into something this package cannot
// report back in its own diagnostic.
var numericOperators = map[string]bool{
	"+=": true, "-=": true, "*=": true, "/=": true, "%=": true,
	"&=": true, "|=": true, "^=": true, "<<=": true, ">>=": true, "&^=": true,
}

// NumericUpdate applies one bounded compound assignment to an addressable
// numeric target.
//
// The caller has already evaluated the target's address and the right operand,
// in that order and exactly once each; this helper never re-evaluates either.
// It decides whether the operation is admissible and only then commits, so a
// rejected update leaves the target holding exactly the value it held before —
// while whatever effects the operands already had remain, because undoing them
// is not this boundary's business.
//
// Native two's-complement wrapping is preserved for a successful integer
// result, matching the engine's typed wrap. Rejection is reserved for what the
// source rejects: an operand the target type cannot carry, division by zero,
// a shift that is not by an unsigned integer count, and a floating-point
// operation whose result the scalar carrier cannot represent.
func NumericUpdate[T Numeric, R Numeric](target *T, rhs R, op string, site ValueSite) error {
	if target == nil {
		return &NumericError{Kind: "TARGET", Message: "nil assignment target", Site: site}
	}
	result, err := numericResult(*target, rhs, op, site)
	if err != nil {
		return err
	}
	*target = result
	return nil
}

func numericResult[T Numeric, R Numeric](left T, right R, op string, site ValueSite) (T, error) {
	var zero T
	if !numericOperators[op] {
		return zero, &NumericError{Kind: "OP", Message: "unsupported compound assignment operator " + op, Site: site}
	}
	operator := strings.TrimSuffix(op, "=")
	if operator == "<<" || operator == ">>" {
		return numericShift(left, right, operator, site)
	}
	converted, err := numericConvert[T](right, site)
	if err != nil {
		return zero, err
	}
	if numericIsFloat[T]() {
		return numericFloatOp(left, converted, operator, site)
	}
	return numericIntegerOp(left, converted, operator, site)
}

// numericFloatOp keeps the engine's rule that a runtime float carrier has no
// representation for a non-finite value, so the operations that would produce
// one are rejected rather than silently storing an infinity or a NaN.
func numericFloatOp[T Numeric](left, right T, operator string, site ValueSite) (T, error) {
	var zero T
	var result T
	switch operator {
	case "+":
		result = left + right
	case "-":
		result = left - right
	case "*":
		result = left * right
	case "/":
		if right == 0 {
			return zero, &NumericError{Kind: "OP", Message: "BASHPP-EUPDATE-NONFINITE: runtime floating-point division by zero is unsupported by the scalar carrier", Site: site}
		}
		result = left / right
	default:
		return zero, &NumericError{Kind: "OP", Message: "BASHPP-EEXPR-OPERAND: unsupported binary operator " + operator, Site: site}
	}
	if f := float64(result); math.IsInf(f, 0) || math.IsNaN(f) {
		return zero, &NumericError{Kind: "OP", Message: "BASHPP-EUPDATE-NONFINITE: runtime floating-point result is unsupported by the scalar carrier", Site: site}
	}
	return result, nil
}

// numericIntegerOp evaluates in a full-width carrier of the target's own
// signedness and converts back, which reproduces native Go wrapping for every
// declared width without needing one generic function per integer kind. Only
// division and remainder are rejected, and only for a zero divisor: every
// other overflow is a defined wrap that the engine also performs.
func numericIntegerOp[T Numeric](left, right T, operator string, site ValueSite) (T, error) {
	var zero T
	divideByZero := &NumericError{Kind: "OP", Message: "BASHPP-EEXPR-DIVZERO: division by zero", Site: site}
	if numericIsSigned[T]() {
		l, r := int64(left), int64(right)
		switch operator {
		case "+":
			return T(l + r), nil
		case "-":
			return T(l - r), nil
		case "*":
			return T(l * r), nil
		case "/":
			if r == 0 {
				return zero, divideByZero
			}
			return T(l / r), nil
		case "%":
			if r == 0 {
				return zero, divideByZero
			}
			return T(l % r), nil
		case "&":
			return T(l & r), nil
		case "|":
			return T(l | r), nil
		case "^":
			return T(l ^ r), nil
		case "&^":
			return T(l &^ r), nil
		}
		return zero, &NumericError{Kind: "OP", Message: "BASHPP-EEXPR-OPERAND: unsupported binary operator " + operator, Site: site}
	}
	l, r := uint64(left), uint64(right)
	switch operator {
	case "+":
		return T(l + r), nil
	case "-":
		return T(l - r), nil
	case "*":
		return T(l * r), nil
	case "/":
		if r == 0 {
			return zero, divideByZero
		}
		return T(l / r), nil
	case "%":
		if r == 0 {
			return zero, divideByZero
		}
		return T(l % r), nil
	case "&":
		return T(l & r), nil
	case "|":
		return T(l | r), nil
	case "^":
		return T(l ^ r), nil
	case "&^":
		return T(l &^ r), nil
	}
	return zero, &NumericError{Kind: "OP", Message: "BASHPP-EEXPR-OPERAND: unsupported binary operator " + operator, Site: site}
}

// numericShift keeps the shift count out of the target's type: a count is not
// an operand of the target's width, so it is neither converted to it nor
// required to fit in it. A negative or fractional count is rejected instead of
// reaching Go's own shift, which would panic rather than report.
func numericShift[T Numeric, R Numeric](left T, right R, operator string, site ValueSite) (T, error) {
	var zero T
	if numericIsFloat[T]() {
		return zero, &NumericError{Kind: "OP", Message: "BASHPP-EEXPR-SHIFT: shift requires integer operands", Site: site}
	}
	count, err := numericShiftCount(right, site)
	if err != nil {
		return zero, err
	}
	if numericIsSigned[T]() {
		if operator == "<<" {
			return T(int64(left) << count), nil
		}
		return T(int64(left) >> count), nil
	}
	if operator == "<<" {
		return T(uint64(left) << count), nil
	}
	return T(uint64(left) >> count), nil
}

func numericShiftCount[R Numeric](right R, site ValueSite) (uint64, error) {
	shiftError := &NumericError{Kind: "OP", Message: "BASHPP-EEXPR-SHIFT: shift count must be an unsigned integer", Site: site}
	switch {
	case numericIsFloat[R]():
		return 0, shiftError
	case numericIsSigned[R]():
		count := int64(right)
		if count < 0 {
			return 0, shiftError
		}
		return uint64(count), nil
	}
	return uint64(right), nil
}

// numericConvert admits the right operand only when the target type carries it
// exactly. Checking the operand rather than only the result is what the engine
// does, and it is the difference between rejecting `tiny += 300` and quietly
// updating with a truncated 44.
func numericConvert[T Numeric, R Numeric](value R, site ValueSite) (T, error) {
	var zero T
	overflows := &NumericError{Kind: "OP", Message: fmt.Sprintf("BASHPP-EEXPR-CONVERT: constant %v overflows %s", value, numericTypeName[T]()), Site: site}
	if numericIsFloat[T]() {
		converted := T(value)
		if f := float64(converted); math.IsInf(f, 0) && !math.IsInf(float64(value), 0) {
			return zero, overflows
		}
		return converted, nil
	}
	if numericIsFloat[R]() {
		f := float64(value)
		if math.IsNaN(f) || math.IsInf(f, 0) || f != math.Trunc(f) {
			return zero, &NumericError{Kind: "OP", Message: fmt.Sprintf("BASHPP-EEXPR-CONVERT: cannot convert %v to %s", value, numericTypeName[T]()), Site: site}
		}
		// The float is integral, so the range check is exact: every bound
		// below is a power of two, and a direct conversion of a float outside
		// the target's range is undefined in Go rather than merely wrong.
		bits, signed := numericBits[T](), numericIsSigned[T]()
		if signed && (f < -math.Ldexp(1, bits-1) || f >= math.Ldexp(1, bits-1)) {
			return zero, overflows
		}
		if !signed && (f < 0 || f >= math.Ldexp(1, bits)) {
			return zero, overflows
		}
		return T(value), nil
	}
	// Both operands are integers. A round trip is not enough on its own,
	// because a sign change can round-trip through the same bit pattern, so
	// the magnitude is compared against the target's own bounds.
	if numericIsSigned[R]() {
		if v := int64(value); v < 0 {
			if !numericIsSigned[T]() || uint64(-(v+1))+1 > uint64(1)<<(numericBits[T]()-1) {
				return zero, overflows
			}
			return T(value), nil
		}
	}
	if uint64(value) > numericMax[T]() {
		return zero, overflows
	}
	return T(value), nil
}

// numericMax is the largest magnitude the target type carries, expressed in a
// full-width carrier so a 64-bit unsigned target is not measured through int.
func numericMax[T Numeric]() uint64 {
	bits := numericBits[T]()
	if numericIsSigned[T]() {
		return uint64(1)<<(bits-1) - 1
	}
	if bits == 64 {
		return math.MaxUint64
	}
	return uint64(1)<<bits - 1
}

func numericKind[T Numeric]() reflect.Kind { return reflect.TypeFor[T]().Kind() }
func numericTypeName[T Numeric]() string   { return valueTypeName(reflect.TypeFor[T]()) }
func numericBits[T Numeric]() int          { return reflect.TypeFor[T]().Bits() }
func numericIsFloat[T Numeric]() bool {
	kind := numericKind[T]()
	return kind == reflect.Float32 || kind == reflect.Float64
}
func numericIsSigned[T Numeric]() bool {
	switch numericKind[T]() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return true
	}
	return false
}
