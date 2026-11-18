package p

import _ "unsafe"

//go:linkname nonexist example.com/other.nonexist
type t int

var x int

func F() int { return x }

//go:linkname x example.com/other.x
//go:linkname x example.com/other.duplicate
