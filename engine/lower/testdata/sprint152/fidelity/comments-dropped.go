// asmcheck

package main

// Doc comment on F.
//
//go:noinline
func F(x int) int { // ERROR "cannot inline F: marked go:noinline$"
	// amd64:"INCQ"
	return x + 1 // trailing comment
}

func main() {}
