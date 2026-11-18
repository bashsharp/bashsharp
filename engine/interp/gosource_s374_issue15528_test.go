//go:build full

package interp_test

import (
	"strings"
	"testing"
)

// Sprint: #374; Story: #1521; Story-ID: 3fd786892dcc
//
// Regression (1): fixedbugs/issue15528.go fails with:
// BASHPP-EINTERFACE-MISSING: *os.File does not implement interface (missing method Write)
// Native pointer types (*os.File) assigned to or asserted as interfaces must have
// their method set resolved through native type metadata.
// Also, closures held in empty interfaces executed via type assertion callee
// f.(func())() must evaluate and dispatch to the closure.
func TestGoSourceS374Issue15528NativePointerInterfaceMethodSet(t *testing.T) {
	src := `package main

import (
	"fmt"
	"io"
	"os"
)

var efaces = [...]struct {
	x interface{}
	s string
}{
	{io.Writer((*os.File)(nil)), "*os.File <nil>"},
	{(interface{})(io.Writer((*os.File)(nil))), "*os.File <nil>"},
}

var clos int

func AssertWriter[T io.Writer]() {}

func main() {
	for i, test := range efaces {
		s := fmt.Sprintf("%[1]T %[1]v", test.x)
		if s != test.s {
			panic(fmt.Sprintf("eface(%d)=%q want %q", i, s, test.s))
		}
	}

	type MyWriter interface {
		Write(p []byte) (n int, err error)
	}

	var uninit *os.File
	var w io.Writer = uninit
	if w == nil {
		panic("expected non-nil interface holding typed nil")
	}

	var iface interface{} = (*os.File)(nil)
	w2, ok := iface.(io.Writer)
	if !ok || w2 == nil {
		panic("expected type assertion to succeed with typed nil writer")
	}

	mw := iface.(MyWriter)
	if mw == nil {
		panic("expected type assertion to MyWriter to succeed")
	}

	AssertWriter[*os.File]()

	var f interface{} = func() { clos++ }
	f.(func())()
	f.(func())()
	f.(func())()
	if clos != 3 {
		panic(fmt.Sprintf("bad closure exec %d", clos))
	}
	fmt.Println("OK")
}
`
	out, stderr, err := runGoSource(t, "issue15528_test", src)
	if err != nil || stderr != "" || !strings.Contains(out, "OK") {
		t.Fatalf("run failed: err=%v stderr=%q out=%q", err, stderr, out)
	}
}
