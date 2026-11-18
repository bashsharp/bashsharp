// Mechanism: type *ast.BasicLit / *ast.BinaryExpr — calling an element of a
// map or slice of functions. The call converter treats every IndexExpr callee
// as a generic instantiation and passes the index to c.typ.
package main

import "fmt"

func one() int    { return 1 }
func ten() int    { return 10 }
func twenty() int { return 20 }

func main() {
	handlers := make(map[string]func() int)
	handlers["one"] = one
	steps := make([]func() int, 2)
	steps[0], steps[1] = ten, twenty
	i := 0
	fmt.Println(handlers["one"]() + steps[i+1]())
}
