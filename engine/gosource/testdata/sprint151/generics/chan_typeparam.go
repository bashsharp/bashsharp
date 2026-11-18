package main

import "fmt"

// ReadAll drains a receive-only channel whose element is a type parameter.
func ReadAll[Elem any](c <-chan Elem) []Elem {
	var r []Elem
	for v := range c {
		r = append(r, v)
	}
	return r
}

// Fill sends into a channel typed by the type parameter and closes it.
func Fill[T any](c chan T, vs ...T) {
	for _, v := range vs {
		c <- v
	}
	close(c)
}

func Ranger[T any]() (chan<- T, <-chan T) {
	c := make(chan T, 4)
	return c, c
}

func main() {
	c := make(chan int, 3)
	Fill(c, 1, 2, 3)
	fmt.Println(ReadAll(c))

	s := make(chan string, 2)
	Fill[string](s, "a", "b")
	fmt.Println(ReadAll[string](s))

	send, recv := Ranger[float64]()
	send <- 1.5
	close(send)
	fmt.Println(ReadAll(recv))
}
