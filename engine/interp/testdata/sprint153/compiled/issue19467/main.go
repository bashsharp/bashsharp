package main

import (
	"./mysync"
	"runtime"
	"strings"
)

func main() {
	var w mysync.WaitGroup
	w.Done()
	f, _ := runtime.CallersFrames(w.PCs).Next()
	if !strings.Contains(f.Function, "test/mysync.") {
		panic(f.Function)
	}
}
