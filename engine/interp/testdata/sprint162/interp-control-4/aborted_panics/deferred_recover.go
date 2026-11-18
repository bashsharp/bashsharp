package main

import "fmt"

// `defer recover()` in the panicking frame is a no-op — recover is the
// deferred call, nothing deferred it — but deferred by a function that is
// itself a deferred call of the unwinding frame it is that function's own
// recover and squelches the panic. A deferred method that first survives a
// nested panic in a callee still recovers the outer one afterwards.
type I interface{ M() }

type deeper struct{}

func (deeper) M() {
	inner()
	fmt.Println("deeper recovers:", recover())
}

type tiny struct{}

func (tiny) M() { panic(112) }

func inner() {
	defer func() {
		fmt.Println("inner recovers:", recover())
	}()
	var i I = tiny{}
	i.M()
}

func mustNotRecover() {
	fmt.Println("later cleanup sees:", recover())
}

func tooEarly() {
	defer mustNotRecover()
	defer recover()
	panic(2)
}

func squelched() {
	defer mustNotRecover()
	defer func() {
		defer recover()
	}()
	panic(4)
}

func nestedThenOuter() {
	var i I = deeper{}
	defer i.M()
	panic(111)
}

func main() {
	defer func() {
		fmt.Println("main recovers:", recover())
	}()
	squelched()
	nestedThenOuter()
	tooEarly()
	fmt.Println("unreachable")
}
