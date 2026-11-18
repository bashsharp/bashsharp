// Reduced from fixedbugs/issue10353.go — bound method value passed as func across goroutine.
package main

type X int

func (x *X) foo() {}

func clos(f func()) func() {
	return func() { f() }
}

func main() {
	c := make(chan bool)
	go func() {
		x := new(X)
		clos(x.foo)()
		c <- true
	}()
	<-c
	println("ok")
}
