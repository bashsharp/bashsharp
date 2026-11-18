// Positive control: declared struct types were already identified by name.
package main

import "fmt"

type X struct{ x int }
type Y struct{ y string }

func main() {
	var v any = X{1}
	_, ok1 := v.(Y)
	_, ok2 := v.(X)
	fmt.Println(ok1, ok2)
	switch v.(type) {
	case Y:
		fmt.Println("Y")
	case X:
		fmt.Println("X")
	}
	x := X{1}
	fmt.Println(v == any(x), v == any(X{2}))
}
