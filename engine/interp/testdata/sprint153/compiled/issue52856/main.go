package main

import "./a"

func Value() any { return struct{ N int }{} }

func main() {
	if _, ok := a.Value().(struct{ N int }); ok {
		panic("package type identity collapsed")
	}
}
