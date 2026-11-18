// Comparing two interface values whose identical dynamic type is not
// comparable is a run-time panic, observed here through recover; values of
// different dynamic types compare unequal without one.
package main

import "fmt"

func F() interface{} { return struct{ _ []int }{} }
func S() any         { return []int{1} }
func M() any         { return map[string]int{} }

func try(name string, f func() bool) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println(name, "recovered:", r)
		}
	}()
	fmt.Println(name, f())
}

func main() {
	try("struct", func() bool { return F() == F() })
	try("slice", func() bool { return S() == S() })
	try("map", func() bool { return M() == M() })
	try("mixed", func() bool { return F() == S() })
	try("int", func() bool { return any(1) == any(1) })
}
