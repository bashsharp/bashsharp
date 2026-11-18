// A const declared without an explicit type but initialized from a value of a
// defined (named) type inherits that type, not the untyped default. Out-of-
// corpus reduction of fixedbugs/bug439.go: `C2 = C1` where C1 has type E, so
// C2.P() resolves E's method. The converter emitted C2 as an untyped constant,
// dropping E, and the emitter rejected the method call with LOWER-ETYPE:
// C2.P undefined (type untyped int has no field or method P).
package main

import "fmt"

type E int

func (e E) P() int { return int(e) + 1 }

const (
	C1 E = 5
	C2   = C1
)

func main() {
	fmt.Println(C1.P(), C2.P())
}
