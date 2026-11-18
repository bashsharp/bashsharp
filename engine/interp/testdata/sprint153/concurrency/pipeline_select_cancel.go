package main

import "fmt"

func gen(out chan<- int, stop <-chan struct{}) {
	defer close(out)
	for i := 1; i <= 5; i++ {
		select {
		case out <- i:
		case <-stop:
			return
		}
	}
}

func double(in <-chan int, out chan<- int, stop <-chan struct{}) {
	defer close(out)
	for {
		select {
		case v, ok := <-in:
			if !ok {
				return
			}
			select {
			case out <- v * 2:
			case <-stop:
				return
			}
		case <-stop:
			return
		}
	}
}

func main() {
	in := make(chan int)
	out := make(chan int)
	stop := make(chan struct{})
	go gen(in, stop)
	go double(in, out, stop)
	sum := 0
	for v := range out {
		fmt.Println(v)
		sum += v
		if sum >= 6 {
			close(stop)
		}
	}
	fmt.Println("done", sum)
}
