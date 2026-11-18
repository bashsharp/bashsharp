// A dependency's function value, carried through an interface and asserted to
// its signature, is called directly in statement position. The callee is a
// computed expression with no name to look up, so the statement dispatcher must
// evaluate it, recognise the native func handle, and invoke it on the
// dependency rather than reporting "computed callee is not a function".
package main

import "log"

func main() {
	log.SetFlags(0)
	var fn interface{} = log.SetPrefix
	fn.(func(string))("PFX: ")
	log.Print("hi")
}
