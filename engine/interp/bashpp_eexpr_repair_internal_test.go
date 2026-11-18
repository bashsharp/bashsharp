package interp

// Sprint: #209; Story: #461; Story-ID: 4ed649697945
//
// Negative pins for the EEXPR mechanism repairs: the widened conversions
// accept exactly the forms Go's checker accepts and nothing more, and the
// Go-source-only claims stay inert for the classic evaluator.

import (
	"go/constant"
	"go/token"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// A constant complex converts to the real types only while its imaginary
// part is zero; a nonzero imaginary part keeps the exact refusal it had.
func TestBashPPConvertScalarComplexImagGate(t *testing.T) {
	r := &Runner{}
	zeroImag := bashPPScalar{value: constant.ToComplex(constant.MakeInt64(2))}
	converted, err := r.bashPPConvertScalar("float64", zeroImag)
	if err != nil {
		t.Fatalf("zero-imag to float64: %v", err)
	}
	if f, _ := constant.Float64Val(converted.value); f != 2 {
		t.Fatalf("zero-imag to float64: got %v", converted.value)
	}
	asInt, err := r.bashPPConvertScalar("int", zeroImag)
	if err != nil {
		t.Fatalf("zero-imag to int: %v", err)
	}
	if n, _ := constant.Int64Val(asInt.value); n != 2 {
		t.Fatalf("zero-imag to int: got %v", asInt.value)
	}
	withImag := bashPPScalar{value: constant.BinaryOp(constant.MakeInt64(1), token.ADD, constant.MakeImag(constant.MakeInt64(2)))}
	if _, err := r.bashPPConvertScalar("float64", withImag); err == nil || !strings.Contains(err.Error(), "BASHPP-EEXPR-CONVERT") {
		t.Fatalf("nonzero-imag to float64 must keep failing, got %v", err)
	}
}

// `[]byte(nil)` materialises the nil slice only for the Go-source dialect;
// the classic evaluator keeps refusing a nil conversion operand exactly as
// before.
func TestBashPPNilSliceConversionGoSourceGate(t *testing.T) {
	target := &syntax.BashPPCollectionType{
		Kind:    "slice",
		Element: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "byte"}},
	}
	conversion := &syntax.BashPPConvertExpr{
		ConvType:     &syntax.Lit{Value: "[]byte"},
		ConvTypeExpr: target,
		X:            &syntax.BashPPIdent{Name: &syntax.Lit{Value: "nil"}},
	}

	classic := &Runner{}
	_, _, handled, err := classic.bashPPConvertToCollection(conversion)
	if !handled {
		t.Fatal("classic: conversion to []byte not claimed")
	}
	if err == nil || !strings.Contains(err.Error(), "BASHPP-EEXPR-NIL") {
		t.Fatalf("classic: nil operand must keep its refusal, got %v", err)
	}

	gosrc := &Runner{bashPPGoSource: true}
	value, meta, handled, err := gosrc.bashPPConvertToCollection(conversion)
	if !handled || err != nil {
		t.Fatalf("gosource: handled=%v err=%v", handled, err)
	}
	if meta == nil || meta.kind != "slice" {
		t.Fatalf("gosource: meta %+v", meta)
	}
	if elements, ok := value.([]any); ok && len(elements) != 0 {
		t.Fatalf("gosource: nil slice must be empty, got %v", value)
	}
}
