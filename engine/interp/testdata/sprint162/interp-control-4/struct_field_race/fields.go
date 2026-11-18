package main

import "fmt"

// Two goroutines write DISTINCT fields of one shared struct and hand each
// other the turn over channels. Go permits this without any other
// synchronization: the fields are separate memory words.
type pair struct {
	sc, rc chan int
	sv, rv int
}

func sender(p *pair, done chan bool) {
	for i := 0; i < 200; i++ {
		p.sv++
		p.sc <- p.sv
	}
	done <- true
}

func receiver(p *pair, done chan bool) {
	for i := 0; i < 200; i++ {
		v := <-p.rc
		p.rv = v
	}
	done <- true
}

func main() {
	p := new(pair)
	p.sc = make(chan int, 4)
	p.rc = p.sc
	done := make(chan bool)
	go sender(p, done)
	go receiver(p, done)
	<-done
	<-done
	fmt.Println(p.sv, p.rv)
}
