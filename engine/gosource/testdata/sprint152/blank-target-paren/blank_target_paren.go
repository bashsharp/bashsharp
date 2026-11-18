// A parenthesized blank identifier on an assignment target must lower like a
// bare blank: `(_) = v` evaluates v and discards it. Out-of-corpus reduction
// of fixedbugs/bug420.go (issue 1757), which the converter rejected with
// LOWER-EUNDEFINED: undefined: _ because the parenthesized target never
// reached the blank-name list.
package main

import "fmt"

func f() int {
	fmt.Println("evaluated")
	return 42
}

func main() {
	(_) = f()
	fmt.Println("ok")
}
