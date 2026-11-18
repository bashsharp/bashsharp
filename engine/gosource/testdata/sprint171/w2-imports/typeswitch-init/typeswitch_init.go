// Outside-corpus reproducer: a type switch with an init statement. Go scopes
// the init to the switch (spec: init, guard and body share an implicit
// block); the shapes below exercise that scope, a labeled break through the
// switch, a goto that re-enters a labeled switch so the init runs again, and
// an init that is not a short declaration.
package main

import "fmt"

var calls int

func next() any {
	calls++
	switch calls {
	case 1:
		return 7
	case 2:
		return "s"
	}
	return nil
}

func main() {
	i := "outer"
	switch i := next(); v := i.(type) {
	case int:
		fmt.Println("int", v, i)
	case string:
		fmt.Println("string", v)
	default:
		fmt.Println("other")
	}
	fmt.Println("after", i)

	// A labeled break names the switch through the scoping block.
outer:
	switch x := next(); x.(type) {
	case string:
		for j := 0; ; j++ {
			if j == 2 {
				fmt.Println("break", j)
				break outer
			}
		}
		fmt.Println("unreachable")
	}

	// A goto re-enters the labeled switch, so its init runs once more.
	n := 0
again:
	switch v := next(); v.(type) {
	case nil:
		n++
		fmt.Println("nil", calls)
		if n < 2 {
			goto again
		}
	}

	// The init need not be a short declaration: an assignment to an
	// enclosing variable is visible after the switch.
	var last any
	switch last = 3; t := last.(type) {
	case int:
		fmt.Println("int again", t)
	}
	fmt.Println("last", last)
}
