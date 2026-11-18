//go:build full

package interp_test

import "testing"

func TestS243PointerStorageReturnCaptureControls(t *testing.T) {
	typedSendThreeModes(t, `package main
import "fmt"

var g int
var untouched = 7

func intptr() *int { return &g }
func left(n int) *int {
	if n == 0 { return intptr() }
	return right(n - 1)
}
func right(n int) *int {
	if n == 0 { return intptr() }
	return left(n - 1)
}
func worker(done chan bool, fn func(int) *int) {
	p := fn(5)
	alias := p
	*alias = 41
	*p++
	g := untouched
	local := &g
	*local = 9
	if g != 9 { panic("shadow") }
	done <- true
}
func main() {
	done := make(chan bool)
	fn := left
	go worker(done, fn)
	<-done
	if g != 42 || untouched != 7 { panic("identity") }
	fmt.Println(g, untouched)
}
`)
}
