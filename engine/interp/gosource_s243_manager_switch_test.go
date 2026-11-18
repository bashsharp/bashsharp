//go:build full

package interp_test

import "testing"

func TestS243ManagerSwitchDynamicIdentity(t *testing.T) {
	typedSendThreeModes(t, `package main
import "fmt"
type A int
func(A) F() {}
type B int
func(B) F() {}
type I interface{F()}
type S []int
func(S) F() {}
func incomparable() {
 defer func() { if recover() == nil { panic("incomparable interface did not panic") } }()
 var i I = S{1}
 switch i { case i: panic("unreachable") }
}
func main() {
 var i I = A(1)
 switch i { case B(1): panic("distinct type matched"); case A(1): fmt.Println("same") }
 switch B(1) { case i: panic("reverse distinct type matched"); default: fmt.Println("distinct") }
 var nilI I
 switch nilI { case nil: fmt.Println("nil"); default: panic("nil interface missed") }
 incomparable()
}`)
}
