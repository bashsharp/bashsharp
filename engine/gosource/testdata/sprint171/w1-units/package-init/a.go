package a

import (
	"runtime"
	"strings"
)

var X = f()

func f() int {
	var b [4096]byte
	n := runtime.Stack(b[:], false)
	if !strings.Contains(string(b[:n]), "a.init") {
		panic("missing a.init")
	}
	return 1
}

func init() {
	println("a.init", X)
}
