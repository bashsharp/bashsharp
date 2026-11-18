//go:build full

package interp_test

import "testing"

// TestS374NativeAddressAssignIntoInterface mirrors go/types bound(): assigning
// the address of a dependency-owned composite literal into an
// interface-typed variable must box the dependency's own pointer handle, the
// same way declaring it (`y := &ast.InterfaceType{...}`) already does.
// Before the fix the assignment was refused with
// `BASHPP-EPOINTER-TARGET: expression is not a pointer`.
func TestS374NativeAddressAssignIntoInterface(t *testing.T) {
	stdout, stderr, err := runGoSourcePackages(t, `package main
import "test/p"
func main() { p.Run() }
`, map[string]string{"a.go": `package p
import (
	"fmt"
	"go/ast"
	"go/token"
)
func wrap(x ast.Expr) ast.Expr {
	x = &ast.InterfaceType{Methods: &ast.FieldList{List: []*ast.Field{{Type: x}}}}
	return x
}
func Run() {
	in := &ast.UnaryExpr{Op: token.ADD, X: &ast.Ident{Name: "int"}}
	out := wrap(in)
	iface, ok := out.(*ast.InterfaceType)
	fmt.Println(ok, len(iface.Methods.List))
}
`})
	if err != nil || stderr != "" || stdout != "true 1\n" {
		t.Fatalf("stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
}
