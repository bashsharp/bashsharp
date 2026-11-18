// Positive control: comparable dynamic types compared as values, and a
// recover of an explicit panic, were already supported.
package main

import "fmt"

type P struct{ A, B int }

func try(name string, f func() bool) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println(name, "recovered:", r)
		}
	}()
	fmt.Println(name, f())
}

func main() {
	try("struct", func() bool { return any(P{1, 2}) == any(P{1, 2}) })
	try("string", func() bool { return any("a") == any("b") })
	try("panic", func() bool { panic("boom") })
}
