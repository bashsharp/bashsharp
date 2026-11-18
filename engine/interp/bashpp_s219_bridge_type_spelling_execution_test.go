//go:build full

// Sprint: #219; Story: #462; Story-ID: e348d2c13248
package interp_test

import (
	"testing"

	"github.com/go-quicktest/qt"
)

func TestS219BridgeTypeSpellingExecution(t *testing.T) {
	src := `package main
import "reflect"

type NamedFunc func() interface{}
type NamedPointer *interface{}

func main() {
	var p *interface{}
	var f func() interface{}
	var namedFunc NamedFunc
	var namedPointer NamedPointer

	println(reflect.TypeOf(p).String())
	println(reflect.TypeOf(f).String())
	println(reflect.TypeOf(namedFunc).String())
	println(reflect.TypeOf(namedPointer).String())
	println(reflect.TypeOf(f) == reflect.TypeOf(namedFunc))
	println(reflect.TypeOf(p) == reflect.TypeOf(namedPointer))
}`
	_, stderr, err := runGoSource(t, "s219-bridge-type-spelling", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, "*interface {}\nfunc() interface {}\nmain.NamedFunc\nmain.NamedPointer\nfalse\nfalse\n"))
}
