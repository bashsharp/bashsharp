package main

import (
	"fmt"
	"runtime"
)

// A stack walk from a deferred call finds the deferring frame at its return
// point: the line of the return statement it executed — also when that
// statement's expression called another function — or the closing brace of a
// body that ran to its end. The positive control is an ordinary callee, which
// finds its caller at the line of the call; while a panic unwinds the
// deferring frame stays at the fault line, as before.

func report(what string) {
	_, _, line, _ := runtime.Caller(1)
	fmt.Println(what, "at line", line)
}

func implicit() {
	defer report("implicit return")
	report("plain call")
}

func explicit() int {
	defer report("explicit return")
	if true {
		return 1
	}
	return 2
}

func value() int {
	report("callee of the return")
	return 7
}

func returnsCall() int {
	defer report("return of a call")
	return value()
}

func loop() {
	for i := 0; i < 2; i++ {
		defer report("loop defer")
	}
}

func nested() {
	defer func() {
		report("nested deferred call")
		func() {
			defer report("literal inside a deferred call")
		}()
	}()
}

type T struct{}

func (T) M() (n int) {
	defer report("method return")
	return 3
}

func panicking() {
	defer func() {
		recover()
		_, _, line, _ := runtime.Caller(2)
		fmt.Println("panicking frame at line", line)
	}()
	panic("boom")
}

func main() {
	implicit()
	explicit()
	returnsCall()
	loop()
	nested()
	T{}.M()
	panicking()
	func() {
		defer report("literal return")
	}()
}
