// Reduced from fixedbugs/bug510.go: a typed nil pointer, (*A)(nil), as the
// argument of an imported call. The dependency bridge has no encoding for
// it (BASHPP-EEXPR-NIL: nil is not a scalar).
package main

import "fmt"

type A = map[int]bool

func main() {
	fmt.Println((*int)(nil) == nil)
	fmt.Println((*A)(nil))
}
