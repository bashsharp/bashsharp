package main

import "fmt"

var sink *int
var m map[int]int
var by []byte

func main() {
	x := 1
	sink = &x
	sink = nil
	m = map[int]int{1: 1}
	m = nil
	by = []byte("ab")
	by = nil
	fmt.Println(sink == nil, m == nil, by == nil, len(by))
	var f func()
	f = func() {}
	f = nil
	fmt.Println(f == nil)
	var i interface{} = 1
	i = nil
	fmt.Println(i == nil)
	var p *int = &x
	p = nil
	fmt.Println(p == nil)
	var e error
	e = fmt.Errorf("x")
	e = nil
	fmt.Println(e == nil)
}
