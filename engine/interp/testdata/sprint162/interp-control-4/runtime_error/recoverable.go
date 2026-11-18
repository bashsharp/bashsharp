package main

import (
	"fmt"
	"runtime"
	"strings"
)

// Every Go run-time error is a panic whose value implements runtime.Error;
// a deferred recover sees it as an error and reads the runtime's wording.
type T struct{ a int }
type E struct{}

func (E) Error() string { return "e!" }

type I interface{ M() }
type MyInt int

func try(name string, f func()) {
	defer func() {
		p := recover()
		e, isErr := p.(error)
		_, isRT := p.(runtime.Error)
		fmt.Println(name, isErr, isRT, p)
		if isErr {
			fmt.Println(" text:", e.Error())
		}
		if isRT {
			fmt.Println(" runtime:", strings.HasPrefix(e.Error(), "runtime error: "))
		}
	}()
	f()
}

var zero int

func main() {
	var x interface{} = 3
	var e error
	var n interface{}
	try("div", func() { fmt.Println(1 / zero) })
	try("mod", func() { fmt.Println(7 % zero) })
	try("assert", func() { _ = x.(T) })
	try("assert-iface", func() { _ = x.(I) })
	try("assert-nil", func() { _ = n.(T) })
	try("assert-error-nil", func() { _ = e.(E) })
	try("panic-nil", func() { panic(nil) })
	try("panic-nil-iface", func() { panic(n) })
	try("named", func() { panic(MyInt(4)) })
	try("string", func() { panic("s") })
	try("ok", func() { _, ok := x.(T); fmt.Println("comma-ok", ok) })
	try("none", func() {})
	var m MyInt = 7
	panic(m)
}
