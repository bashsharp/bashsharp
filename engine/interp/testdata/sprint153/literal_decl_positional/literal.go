// A short declaration from a literal inside a function whose arguments are
// closures: `i := 1` is the number 1, not the function's first argument.
package main

import "fmt"

func run(yield func(string) bool) {
	for i := 1; i < 3; i++ {
		fmt.Println("run", i)
		yield("x")
	}
	n := 2
	fmt.Println("n", n)
}

func two(f func() int, g func() int) int {
	a := 1
	b := 2
	return a*f() + b*g()
}

func main() {
	run(func(v string) bool { fmt.Println("got", v); return true })
	fmt.Println(two(func() int { return 10 }, func() int { return 100 }))
}
