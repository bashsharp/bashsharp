// A predeclared type name may be redeclared by the user as a package-level
// const. The converter must not then materialize an untyped constant through
// that name: `int(x)` would call the const, not convert to the type. Out-of-
// corpus reduction of rename.go, which the converter rejected with LOWER-ETYPE:
// invalid operation: cannot call int (untyped int constant 15): untyped int is
// not a function, because expr() wrapped the sum in int(...).
package main

import "fmt"

const (
	int = 15
	NUM = 2
)

func main() {
	n := int + NUM
	fmt.Println(n)
}
