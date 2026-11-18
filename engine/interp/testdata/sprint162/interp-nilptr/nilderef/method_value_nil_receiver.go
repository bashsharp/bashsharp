package main

import "fmt"

type T1 struct{ T2 }
type T2 struct{ *T3 }
type T3 struct{ *T4 }
type T4 struct{}

func (t4 T4) M(x int, b byte) (byte, int) { return b, x + 40 }

var f func(int, byte) (byte, int)

func shouldPanic(fn func()) {
	defer func() {
		if recover() == nil {
			panic("not panicking")
		}
	}()
	fn()
}

func main() {
	shouldPanic(func() { var t1 T1; f = t1.M })
	shouldPanic(func() { var t2 T2; f = t2.M })
	shouldPanic(func() { var t3 *T3; f = t3.M })
	shouldPanic(func() { var t3 T3; f = t3.M })
	if f != nil {
		panic("something set f")
	}
	fmt.Println("ok")
}

type Tsmallv byte

func (v Tsmallv) M(x int, b byte) (byte, int) { return b, x + int(v) }

type Tsmallp byte

func (p *Tsmallp) M(x int, b byte) (byte, int) { return b, x + int(*p) }

type Tinter interface {
	M(int, byte) (byte, int)
}

func init() {
	var psv *Tsmallv
	shouldPanic(func() { f = psv.M })
	shouldPanic(func() { var i Tinter; f = i.M })
	var psp *Tsmallp
	psp = nil
	f = psp.M // a pointer-receiver method value through nil does not fault
	if f == nil {
		panic("nothing set f")
	}
	f = nil
	defer func() {
		fmt.Println("value method:", recover())
	}()
	f = psv.M
}
