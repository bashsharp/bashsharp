package main

import (
	"fmt"
	"runtime"
)

// A frame is named as Go names it: a declared function `main.f`, a method
// `main.T.M` or `main.(*T).P`, a generic function `main.gen[...]` and a method
// of a generic type `main.Box[...].Get`; a function literal after the
// declaration it is written in — `main.f.func1`, `main.f.func2` in source
// order, whether or not an earlier literal has run — and a literal inside a
// literal `main.f.func1.1`; a package-level literal `main.init.func1`. The
// positive control is the declared function's own name.

func name() string {
	pc, _, _, _ := runtime.Caller(1)
	return runtime.FuncForPC(pc).Name()
}

var global = func() string { return name() }

type T struct{}

func (T) M() string  { return func() string { return name() }() }
func (*T) P() string { return func() string { return name() }() }

type Box[E any] struct{ v E }

func (b Box[E]) Get() string { return name() }

func gen[E any](x E) string { return name() }

func outer() {
	first := func() string { return name() }
	second := func() string {
		inner := func() string {
			deepest := func() string { return name() }
			return deepest() + " " + name()
		}
		sibling := func() string { return name() }
		return name() + " " + inner() + " " + sibling()
	}
	third := func() string { return name() }
	fmt.Println(second())
	fmt.Println(first())
	fmt.Println(third())
	defer func() { fmt.Println(name()) }()
}

func main() {
	fmt.Println(name())
	fmt.Println(global())
	fmt.Println(T{}.M(), (&T{}).P())
	fmt.Println(Box[int]{1}.Get(), gen("s"))
	outer()
	fmt.Println(func() string { return name() }())
}
