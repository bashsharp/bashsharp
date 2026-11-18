package a

type T struct{}

func (T) m() { println("FAIL") }

type I interface{ m() }

type V struct{ i int }
