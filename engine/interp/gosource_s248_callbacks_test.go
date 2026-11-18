//go:build full

package interp_test

// Sprint: #248; Story: #700; Story-ID: 14e8b88629e0

import (
	"strings"
	"testing"
)

// TestS248MakeFuncHandleSliceCallback drives an original func([]reflect.Value)
// []reflect.Value through reflect.MakeFunc. The parameter arrives as the
// dependency's own []reflect.Value behind one handle; the result slice is
// rebuilt from its element handles, which reflect copies out without retaining.
// The made functions are called from their owner and from concurrent tasks;
// each task's callbacks are routed to its own request, so every task sees the
// results of its own arguments.
func TestS248MakeFuncHandleSliceCallback(t *testing.T) {
	const source = `package main

import (
	"fmt"
	"reflect"
)

func sub(args []reflect.Value) []reflect.Value {
	type A struct {
		s int
		t int
	}
	return []reflect.Value{reflect.ValueOf(A{1, 2})}
}

func add(args []reflect.Value) []reflect.Value {
	return []reflect.Value{reflect.ValueOf(int(args[0].Int() + args[1].Int())), reflect.ValueOf(len(args))}
}

func main() {
	f := reflect.MakeFunc(reflect.TypeOf((func() interface{})(nil)), sub).Interface().(func() interface{})
	fmt.Println(f())
	g := reflect.MakeFunc(reflect.TypeOf((func(int, int) (int, int))(nil)), add).Interface().(func(int, int) (int, int))
	fmt.Println(g(2, 3))
	c := make(chan bool, 4)
	for i := 0; i < 4; i++ {
		go func() {
			for j := 0; j < 5; j++ {
				if fmt.Sprint(f()) != "{1 2}" {
					panic("wrong result")
				}
				if sum, n := g(i, j); sum != i+j || n != 2 {
					panic("wrong task result")
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
	out, stderr, err := runGoSource(t, "s248-makefunc", source)
	if err != nil || out != "{1 2}\n5 2\ndone\n" || stderr != "" {
		t.Fatalf("run=%v stdout=%q stderr=%q", err, out, stderr)
	}
}

// A []reflect.Value result is rebuilt on the dependency side, so it is only
// admitted for a consumer that copies results out and never retains the
// slice. Any other consumer keeps the prompt signature refusal.
func TestS248CopiedHandleSliceResultRefusal(t *testing.T) {
	const source = `package main

import (
	"fmt"
	"reflect"
	"sync"
)

func values() []reflect.Value { return []reflect.Value{reflect.ValueOf(1)} }

func main() {
	get := sync.OnceValue(values)
	fmt.Println(len(get()))
}
`
	got := runGoSourceRunnerError(t, source)
	if !strings.Contains(got, "original callback signature requires value-semantics parameters and supported results") {
		t.Fatalf("copied handle-slice result reached a retaining consumer: %q", got)
	}
}

// A made function launched as a go statement runs on a concurrent task. Its
// implementation blocks sending on the captured channel until main receives;
// that callback is routed to the task's own request and holds no gate the
// owner's later MakeFunc calls need.
func TestS248MakeFuncBlockingCallbackTasks(t *testing.T) {
	const source = `package main

import (
	"fmt"
	"reflect"
)

const N = 3

func main() {
	c := make(chan bool, N)
	for i := 0; i < N; i++ {
		f := reflect.MakeFunc(reflect.TypeOf(((func(*int))(nil))),
			func(args []reflect.Value) []reflect.Value {
				c <- true
				return nil
			}).Interface().(func(*int))
		go f(nil)
		g := reflect.MakeFunc(reflect.TypeOf(((func())(nil))),
			func(args []reflect.Value) []reflect.Value {
				c <- true
				return nil
			}).Interface().(func())
		go g()
	}
	for i := 0; i < N*2; i++ {
		<-c
	}
	fmt.Println("done")
}
`
	out, stderr, err := runGoSource(t, "s248-makefunc-blocking", source)
	if err != nil || out != "done\n" || stderr != "" {
		t.Fatalf("run=%v stdout=%q stderr=%q", err, out, stderr)
	}
}
