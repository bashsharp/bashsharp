package main

func main() {
	var receiveOnly <-chan int
	nested := make(chan (<-chan int), 1)
	nested <- receiveOnly
	if got := <-nested; got != receiveOnly {
		panic("wrong receive-only channel")
	}

	plain := make(chan chan int, 1)
	value := make(chan int)
	plain <- value
	if got := <-plain; got != value {
		panic("wrong plain channel")
	}
	println("ok")
}
