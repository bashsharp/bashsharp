package main

import "fmt"

// A function called from a deferred call while an outer panic unwinds is
// not itself being abandoned: it runs to completion and returns its
// results, and the deferred call binds them.
func note() int { return 7 }

func pair() (int, string) { return 1, "one" }

func main() {
	defer func() {
		fmt.Println("recovered:", recover())
	}()
	defer func() {
		n := note()
		a, b := pair()
		fmt.Println("note:", n, a, b, note()+note())
	}()
	panic("x")
}
