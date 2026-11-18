package main

import "fmt"

// A parameter or result declared with a named pointer type (`type PS *dch`)
// binds the pointer it receives, as one declared *dch does: the value is
// dereferenced through the name, compared with nil, passed on to a *dch
// parameter, and a nil one faults on dereference.

type dch struct {
	req chan int
	nam int
}

type PS *dch

var Ones PS

func mkdch(n int) *dch {
	d := new(dch)
	d.req = make(chan int, 1)
	d.nam = n
	return d
}

func Rep(n int) PS {
	Z := mkdch(n)
	return Z
}

func direct(n int) PS { return mkdch(n) }

func get(in *dch) int { return in.nam }

func check(U PS) string {
	return fmt.Sprint(U == nil, " ", get(U), " ", U.nam)
}

func viaChannel(c chan PS) PS { return <-c }

func main() {
	local := Rep(1)
	fmt.Println(local == nil, local.nam, check(local))
	Ones = Rep(2)
	fmt.Println(Ones == nil, check(Ones))
	fmt.Println(check(direct(3)))
	c := make(chan PS, 1)
	c <- Ones
	fmt.Println(viaChannel(c).nam)
	var p PS = mkdch(4)
	p.nam++
	fmt.Println(get(p), p.req != nil)
	defer func() { fmt.Println("nil PS recovered:", recover() != nil) }()
	var none PS
	fmt.Println(none == nil)
	check(none)
}
