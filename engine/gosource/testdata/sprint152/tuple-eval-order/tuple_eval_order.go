// A comma-ok type assertion assigned into a pointer-indirection target must
// keep Go's left-to-right evaluation: the left operand (target) is evaluated
// before the right-hand value. The converter's tuple-assignment split for
// non-identifier targets used to evaluate the right-hand side first and the
// left operand second, printing "value" before "target". Reduction of the
// REVIEW-132 CONCERN.
package main

import "fmt"

var slot int

func target() *int { fmt.Println("target"); return &slot }
func value() any   { fmt.Println("value"); return 1 }

func main() {
	var ok bool
	*target(), ok = value().(int)
	fmt.Println(slot, ok)
}
