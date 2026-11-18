//go:build full

package interp_test

import "testing"

// Sprint: #270; Story: #761; Story-ID: c8365c7b8c50

// //go:nointerface keeps a method out of interface method sets under
// GOEXPERIMENT=fieldtrack (fixedbugs/issue47928.go, typeparam/mdempsky/15.go)
// and is ignored without it, as cmd/compile's pragmaFlag does.
func TestS270G6NointerfaceFollowsFieldtrack(t *testing.T) {
	src := `package main

type T struct{ U }
type U struct{}

//go:nointerface
func (*U) Bad() {}
func (*U) Good() {}

type G[X any] struct{ U }

//go:nointerface
func (G[X]) GBad() {}

func main() {
	var i interface{} = new(T)
	_, bad := i.(interface{ Bad() })
	_, good := i.(interface{ Good() })
	var g interface{} = G[int]{}
	_, gbad := g.(interface{ GBad() })
	(&U{}).Bad()
	_ = (*U).Bad
	println(bad, good, gbad)
}`
	for experiment, want := range map[string]string{
		"fieldtrack":              "false true false\n",
		"":                        "true true true\n",
		"fieldtrack,nofieldtrack": "true true true\n",
	} {
		t.Run("GOEXPERIMENT="+experiment, func(t *testing.T) {
			t.Setenv("GOEXPERIMENT", experiment)
			out, stderr, err := runGoSource(t, "s270-g6-nointerface", src)
			if err != nil || out != "" || stderr != want {
				t.Fatalf("err=%v out=%q stderr=%q want stderr %q", err, out, stderr, want)
			}
		})
	}
}

// A panic(string) raised inside a native dependency call is an ordinary Go
// panic its caller can recover (fixedbugs/issue28748.go); it used to end the
// program as a bridge failure.
func TestS270G6DependencyStringPanicRecovers(t *testing.T) {
	differGoSource(t, `package main

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"
)

func main() {
	func() {
		defer func() { e := recover(); fmt.Printf("%T %v\n", e, strings.HasPrefix(fmt.Sprint(e), "regexp: Compile")) }()
		regexp.MustCompile("(")
	}()
	func() {
		defer func() { e := recover(); fmt.Printf("%T %s\n", e, e) }()
		f := reflect.MakeFunc(reflect.TypeOf(func() error { return nil }), func([]reflect.Value) []reflect.Value {
			var x [1]reflect.Value
			return x[:]
		}).Interface().(func() error)
		f()
	}()
	func() {
		defer func() { e := recover(); fmt.Printf("%T %s\n", e, e) }()
		f := reflect.MakeFunc(reflect.TypeOf(func() int { return 0 }), func([]reflect.Value) []reflect.Value { return nil }).Interface().(func() int)
		f()
	}()
	fmt.Println("done")
}`, nil, "")
}
