package main

func Add(a, b int) int {
	return a + b
}

func (v Vec) Dot(w Vec) float64 {
	return v.x*w.x + v.y*w.y
}

type Vec struct{ x, y float64 }

func main() {
	println(Add(1, 2), Vec{1, 2}.Dot(Vec{3, 4}))
}
