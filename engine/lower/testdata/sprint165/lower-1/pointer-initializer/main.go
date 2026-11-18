package main

func address(value int) *int { return &value }

var returned *int = address(7)

var seed = 9
var direct *int = &seed

func main() {
	println(*returned, *direct)
}
