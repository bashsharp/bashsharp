package main

func Add(a, b int) int

func Twice(x int) int {
	return Add(x, x)
}

func main() {
	println(Twice(21))
}
