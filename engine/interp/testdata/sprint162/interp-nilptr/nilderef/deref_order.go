package main

import "fmt"

type X struct{ c chan int }

func other() chan int {
	fmt.Println("BUG: other must not be called")
	return make(chan int)
}

func main() {
	defer func() {
		fmt.Println("recovered:", recover() != nil)
	}()
	var x *X
	select {
	case <-x.c:
	case <-other():
	}
}
