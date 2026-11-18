package main

import "fmt"

// A deferred call running for a panic is an ordinary statement sequence: a
// goroutine it starts runs, and a channel receive waiting on that goroutine
// completes, before the unwind continues to the recovering call.

func spawn(name string) {
	c := make(chan string)
	go func() {
		c <- name + " ran"
	}()
	fmt.Println(<-c)
}

var g *int64

func main() {
	defer func() {
		fmt.Println("recovered:", recover() != nil)
	}()
	defer spawn("named")
	defer func() {
		done := make(chan bool)
		go func() {
			fmt.Println("literal ran")
			done <- true
		}()
		<-done
	}()
	*g = 0
	fmt.Println("unreachable")
}
