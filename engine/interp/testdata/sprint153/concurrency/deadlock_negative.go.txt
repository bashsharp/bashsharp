package main

type item struct {
	n int
}

func main() {
	c := make(chan item)
	<-c
}
