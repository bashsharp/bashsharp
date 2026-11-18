package main

import "fmt"

// The blank identifier binds nothing: `var _ = f()` may be spelled any
// number of times in one block, at package level and inside a function,
// and each initializer still runs, in order.
func note(s string) int {
	fmt.Print(s)
	return len(s)
}

var (
	_     = note("a")
	_     = note("b")
	_ int = note("c")
)

func main() {
	var _ = note("d")
	var _ = note("e")
	{
		var _ int = note("f")
		var _ int = note("g")
	}
	_ = note("h")
	fmt.Println()
}
