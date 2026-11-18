//go:build full

package interp_test

import "testing"

// Sprint: #374; Story: #1520; Story-ID: 99641482cdba

// A function returning without an explicit return statement after recovering
// from a panic in a deferred call yields its declared zero value. Unnamed
// result slots must instantiate from their declared type rather than decaying
// into untyped empty string cells that fail typed scalar operations.
func TestGoSourceS374Bug266RecoverUnnamedResultZeroValue(t *testing.T) {
	const source = `package main

func f() int {
	defer func() {
		recover()
	}()
	panic("oops")
}

func g() int {
	return 12345
}

func main() {
	g()
	x := f()
	if x != 0 {
		panic(x)
	}
}
`
	differGoSource(t, source, nil, "")
}
