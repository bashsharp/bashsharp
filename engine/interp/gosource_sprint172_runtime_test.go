//go:build full

package interp_test

import "testing"

func TestGoSourceSprint172SliceFaults(t *testing.T) {
	differGoSource(t, `package main
import "fmt"
func probe(low, high, max int, full bool) {
 defer func() { if p := recover(); p != nil { fmt.Println(p.(error).Error()) } }()
 s := make([]int, 2, 4)
 if full { fmt.Println(len(s[low:high:max])) } else { fmt.Println(len(s[low:high])) }
}
func main() {
 probe(1, 3, 4, false)
 probe(0, 5, 4, false)
 probe(3, 2, 4, false)
 probe(-1, 2, 4, false)
 probe(0, -1, 4, false)
 probe(0, 2, 5, true)
 probe(0, 3, 2, true)
 probe(2, 1, 3, true)
 probe(0, 1, -1, true)
 probe(0, -1, 3, true)
 probe(-1, 1, 3, true)
}`, nil, "")
}

func TestGoSourceSprint172NilReceiver(t *testing.T) {
	differGoSource(t, `package main
import ("fmt"; "runtime/debug"; "strings")
type item struct { n int }
func (p *item) touch() { p.n = 1 }
func (p *item) nilOK() bool { return p == nil }
type outer struct { item }
func direct() { var p *item; (*p).touch() }
func promoted() { var p *outer; p.touch() }
func catch(f func()) {
 defer func() {
  fmt.Println(recover().(error).Error())
  stack := string(debug.Stack())
  fmt.Println(strings.Contains(stack, "main.direct") || strings.Contains(stack, "main.promoted"), strings.Contains(stack, ".touch("))
 }()
 f()
}
func main() { catch(direct); catch(promoted); var p *item; fmt.Println(p.nilOK()); p = &item{}; p.touch(); fmt.Println(p.n) }
`, nil, "")
}

func TestGoSourceSprint172SelectorFaultPosition(t *testing.T) {
	differGoSource(t, `package main
import ("fmt"; "runtime"; "strings")
type action interface { Run() }
func main() {
 defer func() {
  if recover() == nil { panic("missing panic") }
  pcs := make([]uintptr, 32)
  frames := runtime.CallersFrames(pcs[:runtime.Callers(0, pcs)])
  for { f, more := frames.Next(); if strings.HasSuffix(f.Function, "main.main") { fmt.Println(f.Line); break }; if !more { panic("missing frame") } }
 }()
 var a action
 a.
 Run()
}
`, nil, "")
}
