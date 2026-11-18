// A computed callee in statement position: an indexed slice or map element,
// a call's result, and a parenthesised value are called for effect exactly
// as they are called for a value.
package main

import "fmt"

var log []string

func record(s string) func() { return func() { log = append(log, s) } }

func main() {
	fs := []func(){func() { fmt.Println("a") }}
	fs[0]()
	get := func() func() { return func() { fmt.Println("b") } }
	get()()
	m := map[string]func(){"c": func() { fmt.Println("c") }}
	m["c"]()
	(fs[0])()
	record("d")()
	fmt.Println(log)
}
