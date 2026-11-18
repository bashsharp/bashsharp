// Positive control: the same computed callees in expression position were
// already supported.
package main

import "fmt"

func main() {
	fs := []func() int{func() int { return 1 }}
	fmt.Println(fs[0]())
	get := func() func() int { return func() int { return 2 } }
	fmt.Println(get()())
	m := map[string]func() int{"c": func() int { return 3 }}
	fmt.Println(m["c"]())
}
