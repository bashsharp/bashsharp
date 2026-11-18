package main

import "./p"

type T struct{ *p.S }
type I interface{ hidden() }

func main() {
	var x any = T{}
	p.Use(x.(p.I))
	if _, ok := x.(I); ok {
		panic("foreign unexported method leaked")
	}
}
