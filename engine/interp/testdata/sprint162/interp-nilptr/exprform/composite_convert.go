package main

import "fmt"

type T struct {
	i    int
	f    float64
	s    string
	next *T
}

type SR struct{ x, y int }
type node SR

func (n node) sum() int { return n.x + n.y }

type Tbigv [2]int

func (v Tbigv) M() int { return v[0] + v[1] }

func main() {
	var t T
	t = T{0, 7, "hi", &t}
	fmt.Println(t.i, t.f, t.s, t.next == &t)
	n := node(SR{1, 2})
	fmt.Println(n.sum())
	bv := Tbigv([2]int{5, 6})
	fmt.Println(bv.M(), bv)
	var sr SR = SR(node{3, 4})
	fmt.Println(sr)
}
