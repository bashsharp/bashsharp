package main

type a []int
type p int

func (x *a) f() (*p, int) {
	n := 0
	for range *x {
		n++
	}
	return (*p)(&n), n
}
func (x *a) g() p    { return (*p).h(x.f()) }
func (x *p) h(int) p { return *x }
func main() {
	x := make(a, 13)
	if x.g() != 13 {
		panic("wrong length")
	}
}
