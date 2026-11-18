//go:build full

package interp_test

// Sprint: #275; Story: #758; Story-ID: 2577207b59b5
import "testing"

// A made function's implementation answers repeated reflect.ValueOf calls of
// plain operands lazily (bashpp_s275_lazy_valueof.go). Each call's own
// operand must still reach the caller, a lazy value used inside the body
// must materialize to the same value, and concurrent tasks must each get
// their own results.
func TestS275LazyValueOfMadeFunc(t *testing.T) {
	const source = `package main

import (
	"fmt"
	"reflect"
)

type P struct {
	X int
	S string
	B [2]bool
}

func pair(args []reflect.Value) []reflect.Value {
	type A struct {
		s int
		t int
	}
	n := int(args[0].Int())
	return []reflect.Value{reflect.ValueOf(A{n, n * 2})}
}

func used(args []reflect.Value) []reflect.Value {
	n := int(args[0].Int())
	v := reflect.ValueOf(P{n, fmt.Sprint("p", n), [2]bool{n%2 == 0, true}})
	w := reflect.ValueOf(P{n + 1, "w", [2]bool{}})
	if v.Field(0).Int() != int64(n) || v.Kind() != reflect.Struct {
		panic("materialized value differs")
	}
	return []reflect.Value{w, v}
}

func main() {
	f := reflect.MakeFunc(reflect.TypeOf((func(int) interface{})(nil)), pair).Interface().(func(int) interface{})
	g := reflect.MakeFunc(reflect.TypeOf((func(int) (P, interface{}))(nil)), used).Interface().(func(int) (P, interface{}))
	for i := 0; i < 3; i++ {
		v := f(i)
		fmt.Printf("%T %v\n", v, v)
		w, p := g(i)
		fmt.Printf("%+v %+v\n", w, p)
	}
	c := make(chan bool, 4)
	for i := 0; i < 4; i++ {
		go func() {
			for j := 0; j < 20; j++ {
				if got, want := fmt.Sprint(f(i*100+j)), fmt.Sprintf("{%d %d}", i*100+j, 2*(i*100+j)); got != want {
					panic("task got " + got + ", want " + want)
				}
				if w, p := g(j); w.X != j+1 || p.(P).S != fmt.Sprint("p", j) {
					panic("task got a wrong struct")
				}
			}
			c <- true
		}()
	}
	for i := 0; i < 4; i++ {
		<-c
	}
	fmt.Println("done")
}
`
	const want = `main.A {0 0}
{X:1 S:w B:[false false]} {X:0 S:p0 B:[true true]}
main.A {1 2}
{X:2 S:w B:[false false]} {X:1 S:p1 B:[false true]}
main.A {2 4}
{X:3 S:w B:[false false]} {X:2 S:p2 B:[true true]}
done
`
	stdout, stderr, err := runGoSource(t, "s275-lazy-valueof", source)
	if err != nil || stdout != want {
		t.Fatalf("stdout=%q stderr=%q err=%v, want %q", stdout, stderr, err, want)
	}
}
