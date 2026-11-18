package main

import "fmt"

func helper(n int) int {
	if n == 0 {
		return 0
	}
	return helper(n-1) + 1
}

func again() {
	defer func() {
		fmt.Println("inner recovered:", recover())
	}()
	panic("inner")
}

func main() {
	fmt.Println("nothing to recover:", recover())
	defer func() {
		// A call made by the deferred function returns normally while the
		// panic is running; the statements after it still run.
		fmt.Println("helper:", helper(50))
		again() // a nested panic recovered inside the call does not stop this defer
		fmt.Println("recovered:", recover())
	}()
	defer func() {
		defer func() {
			fmt.Println("nested defer sees:", recover())
		}()
		panic("second")
	}()
	var p *int
	*p = 1
}
