package main

import "fmt"

// A function value boxed in an interface keeps its func type: comparing it
// panics with the runtime's wording, whether it is a nil func variable, a
// literal or a declared function; a type switch still sees the func type.
func cmp(x interface{}) bool {
	return x == x
}

func try(name string, x interface{}) {
	defer func() {
		fmt.Println(name, "recovered:", recover())
	}()
	fmt.Println(name, cmp(x))
}

func kind(x interface{}) string {
	switch x.(type) {
	case func():
		return "func()"
	case func(int) int:
		return "func(int) int"
	case nil:
		return "nil"
	}
	return "other"
}

func g() {}

func main() {
	var f func()
	var h func(int) int
	try("int", 1)
	try("string", "s")
	try("nilfunc", f)
	try("closure", func() {})
	try("named", g)
	try("nil", nil)
	fmt.Println(kind(f), kind(h), kind(g), kind(1), kind(nil))
	var m map[int]int
	var s struct{ x []int }
	try("map", m)
	try("struct", s)
}
