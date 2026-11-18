package main

type M interface{ M() }
type M0 struct{ p *int }

func (M0) M() {}

func F(x M, y any) (M0, bool) {
	v1 := x.(M0)
	var v int
	var ok bool
	v, *(&ok) = y.(int)
	_ = v
	return v1, ok
}

func main() {}
