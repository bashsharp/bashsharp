package main

import (
	"fmt"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
)

// runtime.Caller, runtime.FuncForPC, runtime.Stack and debug.Stack report the
// interpreted frames: the function running at each level, at the line it is
// executing, and — while a deferred call runs for a panic — the panicking
// frames at the fault line, until the recovering call returns.

type T struct{ val int }

type Inner struct{}

func (i *Inner) NotExpectedInStackTrace() int {
	if i == nil {
		return 86
	}
	return 17
}

type Outer struct{ Inner }

func where() (string, int, string) {
	pc, file, line, ok := runtime.Caller(0)
	if !ok {
		return "", 0, ""
	}
	return filepath.Base(file), line, runtime.FuncForPC(pc).Name()
}

func caller() (string, int, string) {
	pc, file, line, ok := runtime.Caller(1)
	if !ok {
		return "", 0, ""
	}
	return filepath.Base(file), line, runtime.FuncForPC(pc).Name()
}

// fault dereferences a nil pointer inside a switch tag: the fault line is the
// tag's line, not the line of a case.
func fault() {
	var p *T
	switch p.val {
	case 0:
		fmt.Println("zero")
	case 1:
		fmt.Println("one")
	}
}

// find walks the stack for the first frame of fn and reports its line.
func find(fn string) int {
	for i := 0; ; i++ {
		pc, _, line, ok := runtime.Caller(i)
		if !ok {
			return -1
		}
		if runtime.FuncForPC(pc).Name() == fn {
			return line
		}
	}
}

func unwind() {
	defer func() {
		fmt.Println("recovered:", recover() != nil)
		fmt.Println("fault at line:", find("main.fault"))
		fmt.Println("unwind at line:", find("main.unwind"))
		fmt.Println("main at line:", find("main.main"))
		_, _, line, _ := runtime.Caller(0)
		fmt.Println("deferred at line:", line)
	}()
	fault()
}

func checkstack() {
	_ = recover()
	var buf [2048]byte
	n := runtime.Stack(buf[:], false)
	s := string(buf[:n])
	fmt.Println("stack has fault line:", strings.Contains(s, "runtime_caller.go:49 "))
	fmt.Println("stack has case line:", strings.Contains(s, "runtime_caller.go:51 "))
	fmt.Println("stack names fault:", strings.Contains(s, "main.fault()"))
	fmt.Println("stack names checkstack first:", strings.HasPrefix(s, "goroutine 1 [running]:\nmain.checkstack()\n"))
}

func withStack() {
	defer checkstack()
	fault()
}

func expected() {
	var o *Outer
	fmt.Println(o.NotExpectedInStackTrace())
}

func withDebugStack() {
	defer func() {
		if recover() == nil {
			return
		}
		trace := string(debug.Stack())
		fmt.Println("expected in trace:", strings.Contains(trace, "main.expected"))
		fmt.Println("not expected in trace:", strings.Contains(trace, "NotExpectedInStackTrace"))
		fmt.Println("goroutine header:", strings.HasPrefix(trace, "goroutine 1 [running]:\n"))
	}()
	expected()
}

func main() {
	fmt.Println(where())
	fmt.Println(caller())
	unwind()
	fmt.Println("after unwind, fault frame:", find("main.fault"))
	withStack()
	withDebugStack()
	_, file, line, ok := runtime.Caller(100)
	fmt.Println("beyond the stack:", file, line, ok)
}
