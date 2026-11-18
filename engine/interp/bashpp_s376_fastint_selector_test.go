//go:build full

package interp_test

import (
	"strings"
	"testing"
)

// Sprint: #376; Story: #1502; Story-ID: ac47500351df
//
// `t.f` over a plain int field is evaluated without the constant carrier. The
// typed-int subset must agree with the general evaluator on every shape that
// reaches it and decline the rest.
func TestS376FastIntSelectorAgreesWithGeneralEvaluator(t *testing.T) {
	out, stderr, err := runGoSource(t, "fastint-selector", `package main
import "fmt"
type ID int
type T struct {
 a, b int
 id ID
 f float64
 in struct{ x int }
}
type E struct{ T }
func mk(k int) *T { return &T{a: k, b: -k, id: ID(k), f: 1.5} }
func main() {
 p := mk(7)
 v := *p
 fmt.Println(p.a == 7, p.b != -7, v.a+v.b, p.a*3-p.b)
 p.a = 9223372036854775807
 fmt.Println(p.a+1 < 0, v.a == 7)
 fmt.Println(p.id == 7, p.f > 1, p.in.x == 0)
 e := E{T: v}
 fmt.Println(e.a == 7, e.b+e.a)
 var n int = 3
 fmt.Println(v.a-n, n*v.a)
 all := []*T{}
 for i := range 100 { all = append(all, mk(i)) }
 bad := 0
 for i, o := range all { if o.a != i || o.b != -i { bad++ } }
 fmt.Println(bad)
 var np *T
 defer func() { fmt.Println("recovered", recover() != nil) }()
 fmt.Println(np.a == 0)
}
`)
	want := "true false 0 28\ntrue true\ntrue true true\ntrue 0\n4 21\n0\nrecovered true\n"
	if err != nil || stderr != "" || out != want {
		t.Fatalf("run=%v stdout=%q stderr=%q want=%q", err, out, stderr, want)
	}
}

func TestS376FastIntSelectorSharedStructAcrossGoroutines(t *testing.T) {
	out, stderr, err := runGoSource(t, "fastint-selector-shared", `package main
import ("fmt"; "sync")
type T struct{ a, b int }
func main() {
 t := &T{}
 var wg sync.WaitGroup
 var mu sync.Mutex
 for g := range 4 {
  wg.Add(1)
  go func() {
   defer wg.Done()
   for i := range 200 {
    mu.Lock()
    if t.a != t.b { fmt.Println("torn", g, i) }
    t.a++
    t.b++
    mu.Unlock()
   }
  }()
 }
 wg.Wait()
 fmt.Println(t.a, t.b, t.a == 800)
}
`)
	if err != nil || stderr != "" || strings.TrimSpace(out) != "800 800 true" {
		t.Fatalf("run=%v stdout=%q stderr=%q", err, out, stderr)
	}
}

func TestS376FastIntSelectorSeesUnsafeAliasWrites(t *testing.T) {
	out, stderr, err := runGoSource(t, "fastint-selector-unsafe", `package main
import ("fmt"; "unsafe")
type T struct{ a, b int }
type U struct{ b, a int }
func main() {
 p := new(T)
 q := (*U)(unsafe.Pointer(p))
 q.a = 9
 q.b = 4
 fmt.Println(p.a == 9, p.a+p.b, p.b != 4)
 v := T{}
 w := (*U)(unsafe.Pointer(&v))
 w.b = 5
 fmt.Println(v.a == 5, v.a*2)
}
`)
	want := "false 13 true\ntrue 10\n"
	if err != nil || stderr != "" || out != want {
		t.Fatalf("run=%v stdout=%q stderr=%q want=%q", err, out, stderr, want)
	}
}
