// A dependency's function value carried through an interface is asserted to its
// own signature, then called. The dependency reports the dynamic type in Go's
// spelling (`func(string)`); the asserted BashPPFuncType must render the same
// way so the exact-match assertion succeeds instead of failing on a spurious
// `func(string)()` spelling.
package main

import "log"

func main() {
	log.SetFlags(0)
	var fn interface{} = log.SetPrefix
	f := fn.(func(string))
	f("PFX: ")
	log.Print("hi")
}
