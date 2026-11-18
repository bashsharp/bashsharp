// A struct field naming an alias of an instantiated generic type. The generic
// declaration cannot be materialised, so the alias and the struct that names
// it must both stay out of the helper instead of breaking its build.
package main

import "fmt"

type entry[T any] struct {
	v T
}

type intEntry = entry[int]

type record struct {
	Label string
	e     intEntry
}

func main() {
	r := record{Label: "kept"}
	r.e = intEntry{v: 6}
	fmt.Println("record", r.Label, r.e.v)
}
