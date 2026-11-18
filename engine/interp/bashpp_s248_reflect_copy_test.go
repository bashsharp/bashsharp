//go:build full

// Sprint: #248; Story: #700; Story-ID: 14e8b88629e0
package interp_test

import (
	"strings"
	"testing"

	"github.com/go-quicktest/qt"
)

// TestS248ReflectCopyTraversal pins read-only reflect traversal over an
// interpreted value whose type carries a mirrored method: reflect.ValueOf
// copies the operand, the traversal (Field/Elem/Interface) stays on that copy,
// and a value-receiver method raised through it runs the original body. A
// pointer the copy carries keeps its authenticated origin, so a native write
// through it is reconciled into the original storage.
func TestS248ReflectCopyTraversal(t *testing.T) {
	t.Run("embedded zero-size method through a copy", func(t *testing.T) {
		src := `package main
import "reflect"
type Renderer interface{ Render() error }
type ZeroSize struct{}
var calls int
func (ZeroSize) Render() error { calls++; return nil }
type Data struct{ X, Y, Z int }
type Container struct {
	ZeroSize
	Data *Data
}
func render(iface any) {
	if reflect.ValueOf(iface).Kind() == reflect.Ptr {
		_ = reflect.ValueOf(iface).Elem().Field(0).Interface().(Renderer).Render()
		return
	}
	_ = reflect.ValueOf(iface).Field(0).Interface().(Renderer).Render()
}
func main() {
	render(Container{})
	render(&Container{})
	println(calls)
}`
		_, stderr, err := runGoSource(t, "s248-reflect-copy-embedded", src)
		qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr=%q", stderr))
		qt.Assert(t, qt.Equals(stderr, "2\n"))
	})

	t.Run("named field method and origin pointer writeback", func(t *testing.T) {
		src := `package main
import "reflect"
type In struct{ v int }
func (i In) V() int { return i.v }
type Emb struct {
	In
	Y In
	P *int
}
func main() {
	n := 5
	var e any = Emb{In{6}, In{7}, &n}
	v := reflect.ValueOf(e)
	println(v.Field(0).Interface().(interface{ V() int }).V(), v.Field(1).Interface().(interface{ V() int }).V())
	v.Field(2).Elem().SetInt(9)
	println(n)
}`
		_, stderr, err := runGoSource(t, "s248-reflect-copy-writeback", src)
		qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr=%q", stderr))
		qt.Assert(t, qt.Equals(stderr, "6 7\n9\n"))
	})
}

// TestS248ReflectCopyRefusals keeps the dependency-mutation refusal for a
// reflect copy that reaches storage no writeback reconciles — a copied slice
// or map beside a mirrored method — and the retention refusal for a method
// value derived from an admitted copy.
func TestS248ReflectCopyRefusals(t *testing.T) {
	for name, tc := range map[string]struct{ src, want string }{
		"copied slice": {`package main
import "reflect"
type In struct{}
func (In) M() {}
type T struct {
	In
	S []int
}
func main() {
	var x any = T{S: []int{1}}
	reflect.ValueOf(x).Field(1).Index(0).SetInt(2)
	println("after")
}`, "original callback with copied slice references is unsupported"},
		"copied map": {`package main
import "reflect"
type In struct{}
func (In) M() {}
type T struct {
	In
	M2 map[string]int
}
func main() {
	var x any = T{M2: map[string]int{"a": 1}}
	reflect.ValueOf(x).Field(1).SetMapIndex(reflect.ValueOf("b"), reflect.ValueOf(2))
	println("after")
}`, "dependency mutation of interpreter-owned references is unsupported for reflect.ValueOf"},
		"retained derived method value": {`package main
import (
	"reflect"
	"time"
)
type T struct{}
func (T) M() {}
type C struct{ T }
func main() {
	var x any = C{}
	f := reflect.ValueOf(x).Field(0).Interface().(interface{ M() }).M
	time.AfterFunc(time.Hour, f)
	println("after")
}`, "unsupported for time.AfterFunc"},
	} {
		t.Run(name, func(t *testing.T) {
			out, stderr, err := runGoSource(t, "s248-reflect-copy-refused", tc.src)
			qt.Assert(t, qt.IsNotNil(err))
			qt.Assert(t, qt.StringContains(err.Error()+stderr, tc.want))
			qt.Assert(t, qt.IsFalse(strings.Contains(out+stderr, "after")))
		})
	}
}
