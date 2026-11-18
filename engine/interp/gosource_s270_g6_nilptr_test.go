//go:build full

// Sprint: #270; Story: #761; Story-ID: c8365c7b8c50

package interp_test

import "testing"

// Slicing or ranging through a nil pointer to an array is a nil dereference in
// Go, whether the operand is a stored variable or a conversion such as
// `(*[1]int)(nil)`. The recovered panic must be the only effect: a declaration
// whose initializer panicked adds nothing to stderr.
func TestS270G6NilArrayPointerSlicePanics(t *testing.T) {
	source := `package main

func shouldPanic(name string, f func()) {
	defer func() {
		if recover() == nil {
			println(name, "did not panic")
			return
		}
		println(name, "panicked")
	}()
	f()
}

var q *[1 << 10]byte

func main() {
	shouldPanic("var-slice", func() {
		var p *[1 << 10]byte = nil
		var x []byte = p[0:]
		_ = x
	})
	shouldPanic("var-slice-bounds", func() {
		var p *[1 << 10]byte = nil
		var _ []byte = p[10 : len(p)-10]
	})
	shouldPanic("short-slice", func() {
		var x *[10]int
		y := x[:]
		_ = y
	})
	shouldPanic("assign-slice", func() {
		var x []byte
		y := &x
		*y = q[0:]
	})
	shouldPanic("conversion-slice", func() {
		println((*[1]int)(nil)[:])
	})
	shouldPanic("conversion-range", func() {
		for i := range (*[1]int)(nil)[:] {
			_ = i
		}
	})
	shouldPanic("conversion-range-value", func() {
		for i, v := range (*[1]int)(nil)[:] {
			_ = i + v
		}
	})
}
`
	differGoSource(t, source, nil, "")
}
