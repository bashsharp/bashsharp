//go:build full

package interp

import (
	"math"
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
)

func TestS243BuiltinExactSpecialScalarValue(t *testing.T) {
	complexValue := complex(math.Inf(1), math.NaN())
	got := bashPPBuiltinExactScalarValue("(+Inf+NaNi)", bashPPNonFiniteComplexScalar(complexValue, "complex128"))
	value, ok := got.(complex128)
	if !ok || !math.IsInf(real(value), 1) || !math.IsNaN(imag(value)) {
		t.Fatalf("non-finite complex scalar became %#v", got)
	}

	got = bashPPBuiltinExactScalarValue("+Inf", bashPPNonFiniteScalar(math.Inf(1), "float64"))
	value64, ok := got.(float64)
	if !ok || !math.IsInf(value64, 1) {
		t.Fatalf("non-finite float scalar became %#v", got)
	}
}

func TestS243BuiltinExactFiniteComplexValue(t *testing.T) {
	for _, tc := range []struct {
		typ  string
		want any
	}{
		{"complex64", complex64(complex(5, -7))},
		{"complex128", complex128(complex(5, -7))},
	} {
		scalar := bashPPScalar{value: bashPPComplexConstant(complex(5, -7)), typ: tc.typ, runtime: true}
		if got := bashPPBuiltinExactScalarValue("(5-7i)", scalar); got != tc.want {
			t.Fatalf("%s finite complex scalar became %#v (%T), want %#v (%T)", tc.typ, got, got, tc.want, tc.want)
		}
	}
}

func TestS243NonFiniteMethodScalarReturnTransport(t *testing.T) {
	r := &Runner{bashPPGoSource: true}
	complexValue := complex(math.Inf(1), math.NaN())
	cell := &bashPPCell{
		vr:                  expand.Variable{Set: true, Kind: expand.String, Str: "(+Inf+NaNi)"},
		typeName:            "complex128",
		nonFiniteComplex:    complexValue,
		hasNonFiniteComplex: true,
	}
	arg := r.goSourceBuiltinCellArg(cell, "source{}.value()")
	value, ok := arg.value.(complex128)
	if !arg.hasScalar || !ok || !math.IsInf(real(value), 1) || !math.IsNaN(imag(value)) {
		t.Fatalf("method scalar result lost in builtin transport: %#v", arg)
	}
}
