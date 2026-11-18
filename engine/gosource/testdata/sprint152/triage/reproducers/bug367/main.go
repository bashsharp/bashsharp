package main

import "example.com/bug367/p"

type T struct{ p.S }
type I interface{ private() }

func main() {
	p.F(T{})
	var v any = T{}
	if _, ok := v.(I); ok {
		panic("should not satisfy main.I")
	}
	if _, ok := v.(p.I); !ok {
		panic("should satisfy p.I")
	}
}
