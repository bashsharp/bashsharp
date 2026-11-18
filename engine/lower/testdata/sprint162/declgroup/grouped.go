package main

var (
	one   = bump(1)
	two   = bump(2)
	three = bump(3)

	four = bump(4)
)

type (
	Celsius    float64
	Fahrenheit float64
)

var single = bump(5)

const (
	A = iota
	B
)

func bump(x int) int { return x + 1 }

func main() {
	var c Celsius
	var f Fahrenheit
	println(one, two, three, four, single, A, B, c, f)
}
