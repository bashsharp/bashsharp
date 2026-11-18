// The predeclared identifiers true and false on the right of a plain
// assignment, including a multi-target one and a struct field target.
package main

import "fmt"

type flags struct{ on bool }

func main() {
	flag := false
	flag = true
	fmt.Println(flag)
	var b bool
	b = false
	fmt.Println(b)
	var x, y bool
	x, y = true, false
	fmt.Println(x, y)
	var f flags
	f.on = true
	fmt.Println(f.on)
}
