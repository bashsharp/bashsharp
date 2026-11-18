package main

import "fmt"

type Number *Number

func zero() *Number          { return nil }
func add1(x *Number) *Number { e := new(Number); *e = x; return e }
func sub1(x *Number) *Number { return *x }
func gen(n int) *Number {
	if n > 0 {
		return add1(gen(n - 1))
	}
	return zero()
}
func count(x *Number) int {
	if x == nil {
		return 0
	}
	return count(sub1(x)) + 1
}
func add(x, y *Number) *Number {
	if y == nil {
		return x
	}
	return add(add1(x), sub1(y))
}
func mul(x, y *Number) *Number {
	if x == nil || y == nil {
		return zero()
	}
	return add(mul(x, sub1(y)), x)
}
func fact(n *Number) *Number {
	if n == nil {
		return add1(zero())
	}
	return mul(fact(sub1(n)), n)
}
func main() { fmt.Println(count(fact(gen(5)))) }
