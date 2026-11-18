package main

type Vec struct{ x, y float64 }

func (v Vec) Dot(w Vec) float64

func (*Vec) Scale(k float64)

func (v Vec) Norm() float64 {
	return v.Dot(v)
}

func main() {
	var v Vec
	v.Scale(2)
	println(v.Norm())
}
