//go:build full

// Sprint: #248; Story: #701; Story-ID: 9464cf0df26e
package interp_test

import (
	"strings"
	"testing"

	"github.com/go-quicktest/qt"
)

// TestS248ReflectedMethodExpression pins a method expression obtained through
// reflect.Type.Method(i).Func: it binds the original receiver storage, and a
// recover in its body stops the panic of the frame that deferred it. A native
// method on the same shape (a promoted dependency method) keeps the refusal.
func TestS248ReflectedMethodExpression(t *testing.T) {
	t.Run("pointer receiver binds original storage", func(t *testing.T) {
		src := `package main
import "reflect"
type C struct{ n int }
func (c *C) Inc() { c.n++ }
func main() {
	c := C{n: 4}
	f := reflect.TypeOf(&c).Method(0).Func.Interface().(func(*C))
	f(&c)
	f(&c)
	println(c.n)
}`
		_, stderr, err := runGoSource(t, "s248-method-expr-pointer", src)
		qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr=%q", stderr))
		qt.Assert(t, qt.Equals(stderr, "6\n"))
	})

	t.Run("deferred method expression recovers", func(t *testing.T) {
		src := `package main
import "reflect"
type T struct{}
func (*T) M() { println("recovered", recover().(int)) }
func run() {
	f := reflect.TypeOf(&T{}).Method(0).Func.Interface().(func(*T))
	defer f(&T{})
	panic(9)
}
func main() {
	run()
	println("after")
}`
		_, stderr, err := runGoSource(t, "s248-method-expr-recover", src)
		qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr=%q", stderr))
		qt.Assert(t, qt.Equals(stderr, "recovered 9\nafter\n"))
	})

	t.Run("promoted native method refused", func(t *testing.T) {
		src := `package main
import (
	"reflect"
	"sync"
)
type L struct {
	sync.Mutex
	n int
}
func (l *L) Touch() { l.n++ }
func main() {
	l := L{}
	m, _ := reflect.TypeOf(&l).MethodByName("Lock")
	m.Func.Interface().(func(*L))(&l)
	println("locked")
}`
		out, stderr, err := runGoSource(t, "s248-method-expr-native", src)
		qt.Assert(t, qt.IsNotNil(err))
		qt.Assert(t, qt.StringContains(err.Error()+stderr, "dependency mutation of interpreter-owned references is unsupported"))
		qt.Assert(t, qt.IsFalse(strings.Contains(out+stderr, "locked")))
	})
}

// TestS248ReflectValueOfTemporary pins reflect.ValueOf over a non-addressable
// value of a local method-bearing type: its method value runs the original
// body on a private copy and recovers the deferring frame's panic. A copy that
// would share a slice with its caller keeps the refusal.
func TestS248ReflectValueOfTemporary(t *testing.T) {
	t.Run("value receiver temporary", func(t *testing.T) {
		src := `package main
import "reflect"
type T [2]int
func (t T) M() { println("recovered", recover().(int), t[0]+t[1]) }
func run() {
	f := reflect.ValueOf(T{3, 4}).Method(0).Interface().(func())
	defer f()
	panic(12)
}
func main() {
	run()
	println("after")
}`
		_, stderr, err := runGoSource(t, "s248-valueof-temporary", src)
		qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr=%q", stderr))
		qt.Assert(t, qt.Equals(stderr, "recovered 12 7\nafter\n"))
	})

	t.Run("formatting a reflected temporary serves its String", func(t *testing.T) {
		src := `package main
import (
	"fmt"
	"reflect"
)
type C struct{ n int }
func (c C) String() string { return fmt.Sprint("c", c.n) }
func main() {
	v := reflect.ValueOf(C{1})
	fmt.Println(v.NumField(), v, v.Interface())
	done := make(chan bool)
	go func() { fmt.Println(v); done <- true }()
	<-done
}`
		stdout, stderr, err := runGoSource(t, "s248-valueof-format", src)
		qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr=%q", stderr))
		qt.Assert(t, qt.Equals(stdout, "1 c1 c1\nc1\n"))
	})

	t.Run("reference-bearing temporary refused", func(t *testing.T) {
		src := `package main
import "reflect"
type S struct{ s []int }
func (v S) M() { v.s[0] = 9 }
func main() {
	backing := []int{1}
	reflect.ValueOf(S{s: backing}).Method(0).Interface().(func())()
	println("ran", backing[0])
}`
		out, stderr, err := runGoSource(t, "s248-valueof-reference", src)
		qt.Assert(t, qt.IsNotNil(err))
		qt.Assert(t, qt.StringContains(err.Error()+stderr, "original callback with copied slice references is unsupported"))
		qt.Assert(t, qt.IsFalse(strings.Contains(out+stderr, "ran")))
	})
}

// TestS248MakeFuncRecover pins a reflect.MakeFunc function over an original
// implementation: deferred directly it recovers the frame's panic; called by
// another deferred function it does not, and the outer panic keeps its
// identity through that cleanup. Handing the made function to another
// dependency API keeps the refusal.
func TestS248MakeFuncRecover(t *testing.T) {
	t.Run("direct and nested", func(t *testing.T) {
		src := `package main
import "reflect"
func direct(args []reflect.Value) []reflect.Value {
	println("direct", recover().(int))
	return nil
}
func outer(args []reflect.Value) []reflect.Value {
	args[0].Interface().(func())()
	return nil
}
func inner(args []reflect.Value) []reflect.Value {
	println("inner nil", recover() == nil)
	return nil
}
func must(x interface{}) {
	v := recover()
	println("must", v == x)
}
func one() {
	f := reflect.MakeFunc(reflect.TypeOf((func())(nil)), direct).Interface().(func())
	defer f()
	panic(15)
}
func two() {
	defer must(16)
	f2 := reflect.MakeFunc(reflect.TypeOf((func(func()))(nil)), outer).Interface().(func(func()))
	f3 := reflect.MakeFunc(reflect.TypeOf((func())(nil)), inner).Interface().(func())
	defer f2(f3)
	panic(16)
}
func main() {
	one()
	two()
}`
		_, stderr, err := runGoSource(t, "s248-makefunc-recover", src)
		qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr=%q", stderr))
		qt.Assert(t, qt.Equals(stderr, "direct 15\ninner nil true\nmust true\n"))
	})

	t.Run("made function handed to a dependency refused", func(t *testing.T) {
		src := `package main
import (
	"reflect"
	"time"
)
func impl(args []reflect.Value) []reflect.Value { return nil }
func main() {
	f := reflect.MakeFunc(reflect.TypeOf((func())(nil)), impl).Interface().(func())
	time.AfterFunc(time.Hour, f)
	println("scheduled")
}`
		out, stderr, err := runGoSource(t, "s248-makefunc-retained", src)
		qt.Assert(t, qt.IsNotNil(err))
		qt.Assert(t, qt.StringContains(err.Error()+stderr, "dependency mutation of interpreter-owned references is unsupported for time.AfterFunc"))
		qt.Assert(t, qt.IsFalse(strings.Contains(out+stderr, "scheduled")))
	})
	t.Run("made function in a concurrent task", func(t *testing.T) {
		src := `package main
import "reflect"
func impl(args []reflect.Value) []reflect.Value { println("ran"); return nil }
func main() {
	done := make(chan bool)
	f := reflect.MakeFunc(reflect.TypeOf((func())(nil)), impl).Interface().(func())
	go func() {
		f()
		done <- true
	}()
	<-done
	println("joined")
}`
		out, stderr, err := runGoSource(t, "s248-makefunc-task", src)
		qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr=%q", stderr))
		qt.Assert(t, qt.Equals(out+stderr, "ran\njoined\n"))
	})
}
