// C3: untyped constants are emitted as written. The converter used to
// materialize every constant through the type-checker's contextual type —
// `i := int(0)`, `case string(".htm"):`, `var x int = int(5)`, `const r = 97`
// — which the fidelity gate rejects and which moved asmcheck rows. The wrap
// now survives only where the form does not reveal the kind: mixed-kind
// arithmetic defaulting into any, and constants materialized by value (iota).
package main

import "fmt"

const (
	r     = 'a'
	f     = 1.0
	third = 1.0 / 3.0
	big   = 9007199254740993.0
	s     = "str"
	shift = 1 << 10
	e0    = iota
	e1
	typed float32 = 1
)

var x = 5
var y = 'b'
var z = 2.5
var w = s + "ing"

func kind(s string) int {
	switch s {
	case ".htm", ".html":
		return 1
	}
	return 0
}

func main() {
	i := 0
	g := 1.5
	if g != 0 {
		i = 3
	}
	ns := []int{3, 4, 5}
	fmt.Println("i=", i, 1.5, ns[i-3:], kind(".htm"), kind(".txt"))
	fmt.Printf("%T %T %T %T %T %T %T\n", r, f, third, s, shift, e1, typed)
	fmt.Printf("%T %T %T %T %T\n", x, y, z, w, 1+2.5)
	fmt.Println(third*3 == 1, big-9007199254740992.0, e0, e1, typed+0.5, r+1, 'a'+1)
	fmt.Println(string([]byte("foo")[1:]), 2*g, g+1, w)
	// Contexts the form does not reveal keep their conversion.
	var n uint64 = 64
	var u uint64 = 18446744073709551615
	bs := []byte("x\n")
	fmt.Println(1<<n, u, bs[1] == '\n', u-1)
}
