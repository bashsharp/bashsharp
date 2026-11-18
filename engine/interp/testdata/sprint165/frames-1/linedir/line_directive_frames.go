package main

import (
	"fmt"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
)

// runtime.Caller, FuncForPC(...).FileLine and the traceback report the file a
// line directive puts in effect at the frame's position: an absolute name as
// written, a cleared name as "??", a relative name as written when the source
// file was named without a directory, else resolved against that directory.
// The physical file is the positive control: a frame no directive governs
// names it, at its own line.

var physicalDir string

// where names the file and line the caller of where is executing.
func where() string {
	_, file, line, ok := runtime.Caller(1)
	if !ok {
		return "runtime.Caller failed"
	}
	return render(file, line)
}

// render spells a file under the physical source directory relative to it,
// so a directive resolved against that directory reads the same wherever
// the program lives.
func render(file string, line int) string {
	if rel, err := filepath.Rel(physicalDir, file); err == nil && !strings.HasPrefix(rel, "..") && filepath.IsAbs(file) {
		file = "<dir>/" + rel
	}
	return fmt.Sprintf("%s:%d", file, line)
}

// self reports runtime.Caller(0) at the call site, through FuncForPC too.
func self(pc uintptr, file string, line int, ok bool) string {
	f, l := runtime.FuncForPC(pc).FileLine(pc)
	if f != file || l != line {
		return "FileLine disagrees with Caller"
	}
	return render(file, line)
}

func main() {
	_, physical, _, _ := runtime.Caller(0)
	physicalDir = filepath.Dir(physical)
	fmt.Println("physical:", where())
	fmt.Println("physical self:", self(runtime.Caller(0)))
	fmt.Println("physical is absolute:", filepath.IsAbs(physical))
//line /foo/bar.go:123
	fmt.Println("absolute:", where())
//line :1
	fmt.Println("cleared:", where())
//line rel.go:7
	fmt.Println("relative:", where())
	fmt.Println("relative next line:", where())
//line :11:22
	fmt.Println("column keeps the name:", where())
	/*line :10*/ fmt.Println("comment cleared:", where())
	/*line foo.go:20*/ fmt.Println("comment relative:", where())
	fmt.Println("after comment directive:", where())
//line c:/foo/bar.go:987
	fmt.Println("colon in the name:", where())
//line /nested/self.go:40
	fmt.Println("self under directive:", self(runtime.Caller(0)))
//line line_directive_frames.go:1000
	fmt.Println("same name relative:", where())
	traced()
}

// traced recovers a fault and reports where the trace places the frames.
func traced() {
	defer func() {
		recover()
		trace := string(debug.Stack())
		fmt.Println("trace names the adjusted fault file:", strings.Contains(trace, "/adjusted/fault.go:500"))
		fmt.Println("trace names the directive at the call:", strings.Contains(trace, "line_directive_frames.go:1012"))
	}()
	fault()
}

// fault is the last declaration: its directive governs nothing else.
func fault() {
	var p *int
//line /adjusted/fault.go:500
	_ = *p
}
