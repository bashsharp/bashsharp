//go:build full

// Sprint: #219; Story: #462; Story-ID: e348d2c13248
//
// Native handle interface assignment. Outside-corpus reductions of the roots:
//
//   - fixedbugs/bug262
//     map[string]error{"x": errors.New("invalid")} stores a private
//     *errors.errorString handle behind the predeclared error interface.
//   - chan/select5
//     bufio.NewWriter(os.Stdout) is passed to an interpreted io.Writer
//     parameter; the *bufio.Writer handle keeps its identity through the
//     interface and its method is called through it.
//
// The negative space stays: a handle whose dynamic type lacks a method, or
// has it with the wrong signature, is refused with the ordinary interface
// error, and a typed nil handle is a non-nil interface as it is in Go.
package interp_test

import (
	"testing"

	"github.com/go-quicktest/qt"
)

// bug262: a private concrete native dynamic type behind error — stored by
// map index assignment and by a map literal — then called through the
// interface, and replaced by the nil error a tuple assignment yields.
func TestS219NativeInterfaceErrorStringInMap(t *testing.T) {
	src := `package main
import (
	"errors"
	"strconv"
)
var trace string
func f() string { trace += "f"; return "abc" }
func h() string { trace += "h"; return "123" }
func i() *int { trace += "i"; var i int; return &i }
func main() {
	mm := make(map[string]error)
	mm["abc"] = errors.New("invalid")
	println(mm["abc"] != nil, mm["abc"].Error())
	*i(), mm[f()] = strconv.Atoi(h())
	println(mm["abc"] == nil, trace)
	lit := map[string]error{"x": errors.New("literal")}
	e := lit["x"]
	println(e.Error(), errors.Is(e, lit["x"]))
}`
	_, stderr, err := runGoSource(t, "s219errmap", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, "true invalid\ntrue ifh\nliteral true\n"))
}

// select5: a *bufio.Writer handle passed to an interpreted io.Writer
// parameter keeps its identity — the buffered bytes reach os.Stdout only when
// the original handle is flushed afterwards.
func TestS219NativeInterfaceBufioWriterParam(t *testing.T) {
	src := `package main
import (
	"bufio"
	"io"
	"os"
	"fmt"
)
func emit(w io.Writer, s string) { fmt.Fprintf(w, "%s\n", s) }
func main() {
	w := bufio.NewWriter(os.Stdout)
	emit(w, "one")
	emit(w, "two")
	println("buffered", w.Buffered())
	w.Flush()
	println("after", w.Buffered())
}`
	stdout, stderr, err := runGoSource(t, "s219bufio", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stdout, "one\ntwo\n"))
	qt.Assert(t, qt.Equals(stderr, "buffered 8\nafter 0\n"))
}

// A typed nil handle is a non-nil interface, and a call through it reaches
// the native method with a nil receiver as Go does.
func TestS219NativeInterfaceTypedNil(t *testing.T) {
	src := `package main
import (
	"io"
	"os"
)
func main() {
	var f *os.File
	var w io.Writer = f
	println(w == nil, f == nil)
	_, err := w.Write([]byte("x"))
	println(err == os.ErrInvalid)
}`
	_, stderr, err := runGoSource(t, "s219typednil", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, "false true\ntrue\n"))
}
