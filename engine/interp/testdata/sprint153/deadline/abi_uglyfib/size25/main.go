package main

import "fmt"

const size = 25

func f(x int, p *int) {
	if x < 2 {
		*p += x
		return
	}
	x -= 3
	g(x+2, p)
	h(x+1, p)
}
func g(x int, p *int) {
	if x < 2 {
		*p += x
		return
	}
	x -= 3
	h(x+2, p)
	k(x+1, p)
}
func h(x int, p *int) {
	if x < 2 {
		*p += x
		return
	}
	x -= 3
	k(x+2, p)
	f(x+1, p)
}
func k(x int, p *int) {
	if x < 2 {
		*p += x
		return
	}
	x -= 3
	f(x+2, p)
	g(x+1, p)
}
func main() { var y int; f(size, &y); fmt.Printf("u(%d)=%d\\n", size, y) }
