package main

var x = 5

//go:noinline
func main() {
	println(x)
}
