//go:build full

package interp_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestS243StaticArrayLengthOriginal(t *testing.T) {
	source, err := os.ReadFile(filepath.Join(runtime.GOROOT(), "test", "fixedbugs", "issue72844.go"))
	if err != nil {
		t.Fatal(err)
	}
	typedSendThreeModes(t, string(source))
}

// TestS243StaticArrayLengthThreeModes distinguishes the static array rule from
// runtime operands. A nil pointer dereference in a constant len/cap is skipped;
// calls that make the expression non-constant still run once and preserve the
// resulting panic order.
func TestS243StaticArrayLengthThreeModes(t *testing.T) {
	typedSendThreeModes(t, `package main

var calls int
var p *[4]int

func array() [4]int { calls++; return [4]int{} }
func pointer() *[4]int { calls++; return nil }

func panics(f func()) (yes bool) {
	defer func() { yes = recover() != nil }()
	f()
	return false
}

func main() {
	println(len(*p), cap(*p), calls)
	println(len(array()), calls)
	println(len(pointer()), calls)
	println(panics(func() { _ = len(*pointer()) }), calls)
}
`)
}
