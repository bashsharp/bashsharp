package main

import "fmt"

// One channel viewed under chan T, <-chan T and chan<- T is the same channel:
// comparison across the direction types is identity, and a different channel
// or a nil channel of either type compares unequal.

type readOnly <-chan int

func main() {
	ch1 := make(chan struct{})
	var ch2 <-chan struct{} = ch1
	var ch3 chan<- struct{} = ch1
	fmt.Println(ch1 == ch2, ch2 == ch1, ch3 == ch1, ch1 != ch2)
	switch ch1 {
	case ch2:
		fmt.Println("narrow case")
	default:
		fmt.Println("bad narrow case")
	}
	switch ch2 {
	case ch1:
		fmt.Println("narrow switch")
	default:
		fmt.Println("bad narrow switch")
	}
	other := make(chan struct{})
	var ro <-chan struct{} = other
	fmt.Println(ch1 == ro, ro == ch2, other == ro)
	var none <-chan struct{}
	var noneBidi chan struct{}
	fmt.Println(ch1 == none, none == noneBidi, none == nil)
	ints := make(chan int, 1)
	var typed readOnly = ints
	fmt.Println(typed == ints, ints == typed)
}
