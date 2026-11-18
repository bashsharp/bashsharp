// An unexported method name is qualified by its package: p.S's private is
// p.private, so a main type embedding p.S satisfies p.I but not main's own
// interface{ private() }, whose method is main.private.
package main

import (
	"fmt"

	"example.com/foreign/p"
)

type T struct{ p.S }
type I interface{ private() }

type Own struct{}

func (Own) private() {}

func main() {
	p.F(T{})
	var v any = T{}
	_, ok := v.(I)
	fmt.Println("T satisfies main.I:", ok)
	_, ok = v.(p.I)
	fmt.Println("T satisfies p.I:", ok)
	fmt.Println("p sees T:", p.Satisfies(v))
	var w any = Own{}
	_, ok = w.(I)
	fmt.Println("Own satisfies main.I:", ok)
	_, ok = w.(p.I)
	fmt.Println("Own satisfies p.I:", ok)
	fmt.Println("p sees Own:", p.Satisfies(w))
	var pi p.I = T{}
	_, ok = pi.(I)
	fmt.Println("p.I value as main.I:", ok)
}
