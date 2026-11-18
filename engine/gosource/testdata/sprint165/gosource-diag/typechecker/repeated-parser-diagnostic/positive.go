package p

type T struct{}

func (T) one() {}

func (t T) named() {}

func variadic(xs ...int) int { return len(xs) }

var _ = variadic(1, 2)
