package p

import _ "unsafe"

var x int

func F() int { return x }

// A linkname after the last declaration is still read by gc.
//
//go:linkname x example.com/other.x
