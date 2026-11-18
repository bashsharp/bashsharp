// The cascade form: dropping the direct referrer must also drop a type that
// only referred to the referrer.
package main

import "fmt"

type entry[T any] struct {
	v T
}

type intEntry = entry[int]

type middle struct {
	e intEntry
}

type outer struct {
	m middle
}

func main() {
	var o outer
	o.m.e.v = 11
	fmt.Println("outer", o.m.e.v)
}
