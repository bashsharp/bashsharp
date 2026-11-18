// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

//go:build full

package interp_test

import (
	"runtime/debug"
	"testing"
)

// Interpreted recursion depth is bounded by memory, not by the host's
// per-goroutine stack limit. The limit is lowered here so that a depth which
// is cheap to run stands many times beyond what one host stack can hold: one
// interpreted level costs several KiB of host stack, so 64 MiB holds only a
// fraction of the 12000 levels each program below reaches.
func TestGoSourceDeepRecursion(t *testing.T) {
	defer debug.SetMaxStack(debug.SetMaxStack(64 << 20))
	for name, tc := range map[string]struct{ source, want string }{
		"scalar": {`package main
import "fmt"
func f(n int) int {
	if n == 0 {
		return 0
	}
	return 1 + f(n-1)
}
func main() { fmt.Println(f(12000)) }`, "12000\n"},
		"return_in_branch": {`package main
import "fmt"
func f(n int) int {
	if n > 0 {
		return f(n-1) + 1
	}
	return 0
}
func main() { fmt.Println(f(12000)) }`, "12000\n"},
		"linked_values": {`package main
import "fmt"
type Number *Number
func is_zero(x *Number) bool { return x == nil }
func add1(x *Number) *Number { e := new(Number); *e = x; return e }
func sub1(x *Number) *Number { return *x }
func gen(n int) *Number {
	var x *Number
	for i := 0; i < n; i++ {
		x = add1(x)
	}
	return x
}
func add(x, y *Number) *Number {
	if is_zero(y) {
		return x
	}
	return add(add1(x), sub1(y))
}
func count(x *Number) int {
	if is_zero(x) {
		return 0
	}
	return count(sub1(x)) + 1
}
func main() { fmt.Println(count(add(gen(6000), gen(6000)))) }`, "12000\n"},
		"defer_and_recover": {`package main
import "fmt"
var unwound int
func f(n int) int {
	defer func() { unwound++ }()
	if n == 0 {
		panic("bottom")
	}
	return 1 + f(n-1)
}
func main() {
	defer func() { fmt.Println(recover(), unwound) }()
	f(5000)
}`, "bottom 5001\n"},
		"goroutine": {`package main
import "fmt"
func f(n int) int {
	if n == 0 {
		return 0
	}
	return 1 + f(n-1)
}
func main() {
	c := make(chan int)
	go func() { c <- f(5000) }()
	fmt.Println(<-c)
}`, "5000\n"},
	} {
		t.Run(name, func(t *testing.T) {
			stdout, stderr, err := runGoSource(t, "deep", tc.source)
			if err != nil || stderr != "" || stdout != tc.want {
				t.Fatalf("stdout=%q stderr=%q err=%v, want stdout %q", stdout, stderr, err, tc.want)
			}
		})
	}
}
