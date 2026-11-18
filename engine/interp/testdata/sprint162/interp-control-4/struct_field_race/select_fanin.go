package main

import "fmt"

// A select fan-in over channels held in shared structs: each sender writes
// its own struct's send-side fields while the selector reads the receive side
// and writes the receive-side fields. Distinct fields, no data race in Go.
type link struct {
	c      chan int
	sv, rv int
	done   bool
}

func send(l *link, n int, fin chan bool) {
	for i := 0; i < n; i++ {
		l.sv++
		l.c <- l.sv
	}
	close(l.c)
	fin <- true
}

func main() {
	const n = 100
	ls := make([]*link, 4)
	for i := range ls {
		ls[i] = &link{c: make(chan int, 2)}
	}
	fin := make(chan bool)
	for _, l := range ls {
		go send(l, n, fin)
	}
	l0, l1, l2, l3 := ls[0], ls[1], ls[2], ls[3]
	open := 4
	for open > 0 {
		select {
		case v, ok := <-l0.c:
			if !ok {
				l0.c, l0.done = nil, true
				open--
			} else {
				l0.rv += v
			}
		case v, ok := <-l1.c:
			if !ok {
				l1.c, l1.done = nil, true
				open--
			} else {
				l1.rv += v
			}
		case v, ok := <-l2.c:
			if !ok {
				l2.c, l2.done = nil, true
				open--
			} else {
				l2.rv += v
			}
		case v, ok := <-l3.c:
			if !ok {
				l3.c, l3.done = nil, true
				open--
			} else {
				l3.rv += v
			}
		}
	}
	for range ls {
		<-fin
	}
	for _, l := range ls {
		fmt.Println(l.sv, l.rv, l.done)
	}
}
