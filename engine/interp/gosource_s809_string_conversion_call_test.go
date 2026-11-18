//go:build full

package interp_test

// Sprint: #281; Story: #809; Story-ID: fac7e14af4a8
//
// `string(f())` converts the byte or rune slice a call returned. No named
// operand carries it, so the conversion reader used to decline and the
// sequence reached the scalar reader as an object: the resulting "string" was
// that object's JSON encoding. cmd/compile/internal/syntax reports directives
// through `s.errorf("%s", string(s.segment()))`, which printed the message as
// the list of its own byte values instead of the text.

import "testing"

// TestS809StringConversionOfCallResult is the scanner directive shape: the
// converted text is printed, formatted through a variadic handler, measured
// and indexed, and the call behind it runs exactly once.
func TestS809StringConversionOfCallResult(t *testing.T) {
	const source = `package main
import "fmt"
type scanner struct {
	buf   []byte
	a, b  int
	calls int
}
func (s *scanner) segment() []byte { s.calls++; return s.buf[s.a:s.b] }
func (s *scanner) errorf(format string, args ...interface{}) string {
	return fmt.Sprintf(format, args...)
}
func main() {
	s := &scanner{buf: []byte("xx//line f.go:1:2yy"), a: 2, b: 17}
	text := string(s.segment())
	fmt.Println(s.errorf("%s", text))
	fmt.Println(s.errorf("%v", text))
	fmt.Println(text, len(text), text[0] == '/')
	fmt.Println(string(s.segment()) == "//line f.go:1:2", s.calls)
}`
	differGoSource(t, source, nil, "")
}

// TestS809StringConversionCallErrorText carries the same converted text in
// error values, the path that put the byte list in the compiler's diagnostics.
func TestS809StringConversionCallErrorText(t *testing.T) {
	const source = `package main
import (
	"errors"
	"fmt"
)
type scanner struct{ buf []byte }
func (s *scanner) segment() []byte { return s.buf }
func main() {
	s := &scanner{buf: []byte("//line f.go:1:2")}
	first := errors.New(string(s.segment()))
	second := fmt.Errorf("parser.go:140:3: %s", string(s.segment()))
	fmt.Printf("%v|%s|%q\n", first, first, first)
	fmt.Println(second)
	fmt.Println(first.Error() == "//line f.go:1:2", len(first.Error()))
}`
	differGoSource(t, source, nil, "")
}

// TestS809StringConversionCallControls keeps every other call-result operand
// on its own reader and each call at one evaluation: a rune, a string, a named
// string, a rune slice, and a plain function whose result is not converted.
func TestS809StringConversionCallControls(t *testing.T) {
	const source = `package main
import "fmt"
var calls int
func code() rune        { calls++; return 'A' + rune(calls) }
func text() string      { calls++; return "plain" }
func runes() []rune     { calls++; return []rune("héllo") }
type named string
func namedText() named  { calls++; return named("nm") }
func main() {
	fmt.Println(string(code()), string(text()), string(runes()), string(namedText()))
	fmt.Println([]byte(text()), calls)
}`
	differGoSource(t, source, nil, "")
}

// TestS809StringConversionNativeCallControls keeps dependency-owned call
// results on the bridge reader, which already converted them correctly.
func TestS809StringConversionNativeCallControls(t *testing.T) {
	const source = `package main
import (
	"bytes"
	"fmt"
	"strings"
)
func lit() string { return "ab,cd" }
func main() {
	var b bytes.Buffer
	b.WriteString("//line f.go:1:2")
	fmt.Println(string(b.Bytes()), len(string(b.Bytes())))
	fmt.Println(string(bytes.ToUpper([]byte("abc"))))
	fmt.Println(strings.Split(lit(), ","))
}`
	differGoSource(t, source, nil, "")
}

// TestS809StringConversionCallShapes keeps the repair on every callee shape
// that produces the sequence: a closure, a func-typed variable, a generic
// identity, a named byte-slice type and a slice of the call's own result.
func TestS809StringConversionCallShapes(t *testing.T) {
	const source = `package main
import "fmt"
type B []byte
func id[T any](v T) T { return v }
func mk() B { return B("//line f.go:1:2") }
func main() {
	seg := func() []byte { return []byte("//line f.go:1:2") }
	var fv func() []rune = func() []rune { return []rune("héllo") }
	fmt.Println(string(seg()), string(fv()))
	fmt.Printf("%v %q\n", string(seg()), string(seg()))
	fmt.Println(string(id(mk())), string(mk()[2:6]), len(string(mk())))
	fmt.Println(fmt.Sprintf("%v", string(mk())))
}`
	differGoSource(t, source, nil, "")
}
