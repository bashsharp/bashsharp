// Positive control: exported method names are shared across packages, so a
// main type embedding p.S satisfies both p.I and main's interface naming
// Public, and a main type with its own unexported method satisfies main's
// own interface.
package main

import (
	"fmt"

	"example.com/own/p"
)

type T struct{ p.S }
type I interface{ Public() string }
type J interface{ private() }

type Own struct{}

func (Own) private() {}

func main() {
	fmt.Println(p.F(T{}))
	var v any = T{}
	_, ok := v.(I)
	fmt.Println("T satisfies main.I:", ok)
	_, ok = v.(p.I)
	fmt.Println("T satisfies p.I:", ok)
	var w any = Own{}
	_, ok = w.(J)
	fmt.Println("Own satisfies main.J:", ok)
}
