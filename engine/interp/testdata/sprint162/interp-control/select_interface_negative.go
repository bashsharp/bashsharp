package main

func main() {
	var value string
	ch := make(chan int, 1)
	ch <- 7
	select {
	case value = <-ch:
	}
}
