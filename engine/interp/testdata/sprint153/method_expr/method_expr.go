// Method expressions on a declared type: T.M and (*T).M are functions whose
// first parameter is the receiver. They are called directly, bound to
// variables, passed as arguments, and applied to promoted methods; the
// receiver argument may itself be a call whose results are spread.
package main

import "fmt"

type p int

func (x p) h(n int) p   { return x + p(n) }
func (x *p) inc(n int)  { *x += p(n) }
func (x p) tag() string { return fmt.Sprintf("p(%d)", int(x)) }

type box struct{ p }

func apply(f func(p, int) p, v p) p { return f(v, 1) }

func receiver() (*p, int) {
	v := p(20)
	return &v, 3
}

func main() {
	v := p(10)
	fmt.Println(p.h(v, 3))
	fmt.Println((*p).h(&v, 3))
	(*p).inc(&v, 5)
	fmt.Println(v)
	f := p.h
	fmt.Println(f(v, 4))
	g := (*p).inc
	g(&v, 5)
	fmt.Println(v)
	var h func(p, int) p
	h = p.h
	fmt.Println(h(v, 2))
	fmt.Println(apply(p.h, v))
	fmt.Println(p.tag(v), (*p).tag(&v))
	fmt.Println(box.tag(box{3}), (*box).h(&box{4}, 1))
	fmt.Println((*p).h(receiver()))
}
