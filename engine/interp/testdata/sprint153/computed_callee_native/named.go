// Positive control: the same native func handle bound to a named variable and
// then called. A bare-identifier callee already resolves the handle through the
// named-cell dispatch path, so this form worked before the computed-callee
// dispatch landed.
package main

import "log"

func main() {
	log.SetFlags(0)
	var fn interface{} = log.SetPrefix
	f := fn.(func(string))
	f("PFX: ")
	log.Print("hi")
}
