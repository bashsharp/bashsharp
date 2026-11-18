package a

import (
	"runtime"
	"strings"
)

var _ = check()

func check() int {
	var buf [2048]byte
	n := runtime.Stack(buf[:], false)
	if !strings.Contains(string(buf[:n]), "a.init") {
		panic("missing a.init")
	}
	return 0
}
