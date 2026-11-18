// Positive control: one function-local type, declared once, was already
// supported.
package main

import "fmt"

func local() int {
	type s struct{ f int }
	return s{4}.f
}

func main() {
	fmt.Println(local())
}
