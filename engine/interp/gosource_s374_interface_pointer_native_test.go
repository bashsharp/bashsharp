//go:build full

package interp_test

import "testing"

// TestS374InterfaceHeldPointerToNativeParameter mirrors syntax.TestVerify's
// Fprint call: an interface holds a pointer to an interpreted local struct and
// then crosses into a native variadic interface parameter. The worker must
// decode the pointer using its concrete type, while preserving its ability to
// satisfy the declared interface target.
func TestS374InterfaceHeldPointerToNativeParameter(t *testing.T) {
	stdout, stderr, err := runGoSourcePackages(t, `package main
import "test/p"
func main() { p.Run() }
`, map[string]string{"a.go": `package p
import "fmt"
type Expr interface { expr() }
type node struct { N int }
func (*node) expr() {}
func printValue(x any) string { return fmt.Sprintf("%T:%v", x, x) }
func Run() { var x Expr = &node{N: 7}; fmt.Println(printValue(x)) }
`})
	if err != nil || stderr != "" || stdout != "*p.node:&{7}\n" {
		t.Fatalf("stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
}
