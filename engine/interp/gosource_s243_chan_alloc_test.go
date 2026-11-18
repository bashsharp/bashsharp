//go:build full

package interp_test

// Sprint: #243; Story: #674; Story-ID: 63073886bfce
import (
	"testing"
)

// TestS243MakeChanCapacityOutOfRange pins the runtime/native bridge behavior
// behind testdir:fixedbugs/bug273.go: a make(chan T, n) whose capacity the Go
// runtime refuses must surface as a recoverable interpreted panic ("makechan:
// size out of range") translated from the interpreter allocation's own
// makechan refusal, never as a host panic escaping recover(). The three-mode
// harness builds the same program with the real toolchain first, so the
// oracle run is the control that these capacities are refused and recoverable
// in original Go; a capacity inside the bound must still allocate and pass a
// value through. Every negative case uses a declared element type, at both
// ends of the legal element range (Go rejects chan element types over 64kB at
// compile time, hence the [1<<16-1]byte shape from the original test).
func TestS243MakeChanCapacityOutOfRange(t *testing.T) {
	typedSendThreeModes(t, `package main
import "fmt"
type cblock [1<<16 - 1]byte
type wblock [1<<15 - 1]byte
var g4 chan cblock
var gw chan wblock
var minus1 = -1
var big int64 = 10 | 1<<46

func shouldfail(f func(), desc string) {
	var failure any
	func() {
		defer func() { failure = recover() }()
		f()
	}()
	if fmt.Sprint(failure) != "makechan: size out of range" {
		panic("wrong panic for " + desc + ": " + fmt.Sprint(failure))
	}
}
func badchancap()     { g4 = make(chan cblock, minus1) }
func bigchancap()     { g4 = make(chan cblock, big) }
func overflowchan()   { g4 = make(chan cblock, 1<<60) }
func widetypchancap() { gw = make(chan wblock, big) }

func main() {
	shouldfail(badchancap, "badchancap")
	shouldfail(bigchancap, "bigchancap")
	shouldfail(overflowchan, "overflowchan")
	shouldfail(widetypchancap, "widetypchancap")
	c := make(chan int, 3)
	c <- 1
	fmt.Println(<-c)
	fmt.Println("all recovered")
}`)
}
