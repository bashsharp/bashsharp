//go:build full

package interp_test

// Sprint: #319; Story: #1089; Story-ID: 3185346bc4b5

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
)

// TestValuesInfo reaches this shape in go/types/decl.go when it copies
// ast.ValueSpec.Values into constDecl.init. Both sides are []ast.Expr, but the
// source slice is dependency-owned while the destination struct is
// interpreter-owned. The assignment must retain the imported element identity.
func TestS319ImportedSliceStructFieldAssignment(t *testing.T) {
	const source = `package main

import (
	"fmt"
	"go/ast"
)

type declaration struct {
	init []ast.Expr
}

func main() {
	spec := &ast.ValueSpec{Values: []ast.Expr{&ast.Ident{Name: "value"}}}
	d := declaration{init: spec.Values}
	fmt.Printf("%T %s\n", d.init[0], d.init[0].(*ast.Ident).Name)
}
`
	got, err := runGoSourceIdentity(t, source, "")
	if err != nil || got.stdout != "*ast.Ident value\n" || got.stderr != "" || got.status != 0 {
		t.Fatalf("outcome=%+v; err=%v", got, err)
	}
}

// A defined slice is not assignable to another defined slice merely because
// their underlying imported element type is identical.
func TestS319ImportedSliceStructFieldRejectsDistinctDefinedType(t *testing.T) {
	const source = `package main

import "go/ast"

type source []ast.Expr
type destination []ast.Expr
type declaration struct { init destination }

func main() {
	var values source
	_ = declaration{init: values}
}
`
	_, err := gosource.Parse(strings.NewReader(source), "negative.go", gosource.Options{RunMain: true})
	if err == nil || !strings.Contains(err.Error(), "cannot use values") {
		t.Fatalf("want distinct defined-slice refusal, got %v", err)
	}
}
