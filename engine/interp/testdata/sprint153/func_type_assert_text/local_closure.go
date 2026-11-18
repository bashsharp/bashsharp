// Positive control: a locally defined function value carried through an
// interface and asserted to the same signature already worked, because both the
// dynamic type and the asserted type came from the interpreter's own spelling.
package main

import "fmt"

func main() {
	var fn interface{} = func(s string) { fmt.Println("got", s) }
	f := fn.(func(string))
	f("hi")
}
