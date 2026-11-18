//go:build full

package interp_test

import "testing"

func TestS374BisectedIssue24488FunctionMethodValue(t *testing.T) {
	const source = `package main
import "fmt"
type Func func()
func (f Func) Foo() { if f != nil { f() } }
func (f Func) Bar() { if f != nil { f() } }
func main() {
	foo := Func(func() { fmt.Println("called") })
	foo = foo.Bar
	foo.Foo()
}`
	got, err := runGoSourceIdentity(t, source, "")
	if err != nil {
		t.Fatalf("Runner: %v; outcome=%+v", err, got)
	}
	if got.stdout != "called\n" || got.stderr != "" || got.status != 0 {
		t.Fatalf("outcome=%+v", got)
	}
}
