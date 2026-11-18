package main

func accept(value uint) uint { return value }

func main() {
	wide := ^uint(0)
	small := uint(1)
	println(accept(wide) > accept(small))
}
