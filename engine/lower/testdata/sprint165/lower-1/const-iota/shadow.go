package main

const Outer = 2

func main() {
	const (
		Control   = iota
		NamedIota = iota
	)
	const (
		First = iota
		iota  = iota
		Second
		Third
	)
	const (
		Double = Outer + Outer
		Quadruple
		Index = iota
	)
	println(Control, NamedIota, First, Second, Third, Double, Quadruple, Index)
}
