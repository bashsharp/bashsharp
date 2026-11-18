//go:build full

package interp_test

import (
	"fmt"
	"testing"
)

// An alias-typed constant retains its readonly carrier when boxed. A new
// variable must not inherit that binding attribute, even when it shadows a
// predeclared builtin. Use a dependency package like ordinary library code.
func TestS319ShadowedBuiltinLocalAssignment(t *testing.T) {
	for _, name := range []string{"len", "cap", "value"} {
		t.Run(name, func(t *testing.T) {
			source := fmt.Sprintf(`package p
 type Token uint
 type token = Token
 const (_ token = iota; dots)
 type node struct { Len any }
 func Run() {
  var x any = &node{Len: 7}
  switch n := x.(type) {
  case *node:
   var %[1]s any = dots
   if %[1]s != dots { panic("initializer lost") }
   if n.Len != nil { %[1]s = n.Len }
   if %[1]s != 7 { panic("assignment lost") }
   %[1]s = "updated"
   if %[1]s != "updated" { panic("second assignment lost") }
  }
  if dots != 1 { panic("source constant changed") }
  if len([]int{1, 2}) != 2 { panic("builtin scope lost") }
  println("ok")
 }
`, name)
			out, stderr := runGoSourceMultiPackage(t, "shadowed_builtin", `package main
 import "example/p"
 func main() { p.Run() }
`, "example/p", "p.go", source)
			if out != "" || stderr != "ok\n" {
				t.Fatalf("stdout=%q stderr=%q", out, stderr)
			}
		})
	}
}
