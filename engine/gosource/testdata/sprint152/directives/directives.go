// C1, converter half: //go:* directives written on func and var
// declarations travel into the Bash++ AST as the declaration's comments, so
// the emitter prints them verbatim above the generated declaration and gc
// applies them as it did to the source. The converter used to validate and
// drop every directive but //go:embed, so `//go:noinline` functions were
// inlined in the generated Go and the asmcheck rows keyed on them failed.
package main

import "fmt"

// Doc comment on x.
//
//go:noinline
func x(a, b int) int { return a + b }

//go:nosplit
//go:norace
func y(a int) int {
	return a * 2
}

//go:registerparams
//go:noinline
func z(int, int, int) {}

type T struct{ n int }

//go:noinline
func (t *T) M() int { return t.n + x(1, 2) }

//go:generate echo not a declaration directive
var v = y(3)

func main() {
	z(1, 2, 3)
	t := &T{n: 4}
	fmt.Println(x(1, 2), v, t.M())
}
