package main

func main() {
	c := make(chan int, 1)
	var v int
	c <- 1
	select {
	case x, ok := <-c:
		_, _ = x, ok
	}
	c <- 1
	select {
	case x, ok := (<-c):
		_, _ = x, ok
	}
	c <- 2
	select {
	case v = <-c:
	}
	c <- 2
	select {
	case v = (<-c):
	}
	c <- 3
	select {
	case <-c:
	}
	c <- 3
	select {
	case (<-c):
	}
	_ = v
}
