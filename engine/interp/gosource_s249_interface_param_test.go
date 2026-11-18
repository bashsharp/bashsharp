//go:build full

package interp_test

import "testing"

// Sprint: #249; Story: #714; Story-ID: 5251dc793b6e
// A concrete value crossing an interface-typed call boundary keeps the
// interface's static type as well as its dynamic payload.
func TestGoSourceS249ConcreteArgumentKeepsInterfaceParameter(t *testing.T) {
	typedSendThreeModes(t, `package main
type stamp struct{ N int }
func inspect(value any) int { got, ok := value.(stamp); if !ok { panic("lost interface") }; return got.N }
func main() { println(inspect(stamp{42})) }
`)
}

func TestGoSourceS249ConcreteReturnKeepsInterfaceResult(t *testing.T) {
	typedSendThreeModes(t, `package main
type stamp struct{ N int }
func produce() any { return stamp{42} }
func main() { got, ok := produce().(stamp); if !ok { panic("lost interface") }; println(got.N) }
`)
}

// Native calls can return concrete values through a result statically typed as
// any. The result is still an interface value before its assertion.
func TestGoSourceS249NativeConcreteResultKeepsInterfaceType(t *testing.T) {
	typedSendThreeModes(t, `package main
import "reflect"
type stamp struct{ N int }
func main() { value := reflect.ValueOf(stamp{42}).Interface(); got, ok := value.(stamp); if !ok { panic("lost interface") }; println(got.N) }
`)
}

func TestGoSourceS249NativeErrorResultKeepsInterfaceType(t *testing.T) {
	typedSendThreeModes(t, `package main
import (
 "go/ast"
 "go/parser"
 "go/token"
 . "go/types"
)
func main() {
 fset := token.NewFileSet()
 file, _ := parser.ParseFile(fset, "x.go", "package x; var _ = missing", 0)
 _, err := new(Config).Check("x", fset, []*ast.File{file}, nil)
 _, ok := err.(Error)
 println(ok)
}
`)
}
