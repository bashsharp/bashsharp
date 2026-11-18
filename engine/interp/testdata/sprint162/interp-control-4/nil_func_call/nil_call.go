package main

import "fmt"

// Calling a nil function value is a run-time error, raised when the call
// runs: directly, deferred (the value is fixed at the defer), or spelled as
// a conversion of nil to a func type. A non-nil function value — a
// variable assigned later, a literal, a declared function — is called.
func try(name string, f func()) {
	defer func() {
		fmt.Println(name, "recovered:", recover())
	}()
	f()
	fmt.Println(name, "ran")
}

func g() { fmt.Println("g called") }

func main() {
	var f func()
	try("direct", func() { f() })
	try("deferred", func() { defer f(); fmt.Println("body ran") })
	try("conversion", func() { ((func())(nil))() })
	try("deferred conversion", func() { defer ((func())(nil))(); fmt.Println("body ran") })
	f = func() { fmt.Println("assigned called") }
	try("assigned", func() { f() })
	try("declared", g)
	try("literal", func() { fmt.Println("literal called") })
}
