//go:build full

package interp_test

// Sprint: #209; Story: #461; Story-ID: 4ed649697945
//
// A Go-source program is vetted by go/types at parse time, which rejects
// every genuinely impossible type assertion (see the negative test below).
// The evaluator's own EASSERT-IMPOSSIBLE pre-check could therefore only
// fire in Go-source mode on assertions go/types had allowed: instantiated
// type parameters, which Go resolves dynamically — comma-ok yields false,
// a type-switch case simply mismatches, and the bare form panics. The
// pre-check is now the classic dialect's guard alone.
//
// Story #461 root classification:
//   - fixedbugs/issue65962.go, typeparam/issue50002.go — this cause; pass.
//   - typeparam/mdempsky/16.go — same rejection cause repaired (the assert
//     now panics and recovers), but the root still fails on a distinct
//     cause: the panic message spells the anonymous interface operand as
//     "interface", not "interface { T() main.T }" (goSourceReflectTypeText
//     has no method-set spelling for interface types).
//   - fixedbugs/bug473.go — distinct cause: ranging over a variadic
//     ...interface{} parameter drops the elements' interface carrier, so
//     `s += v.(int)` fails with EASSERT-OPERAND (a plain []interface{}
//     range works).
//   - fixedbugs/bug510.go, typeparam/issue47740b.go — distinct cause: a
//     native call result (reflect's .Interface()) carries no interface
//     value, so asserting it fails with EASSERT-OPERAND.

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/go-quicktest/qt"

	"github.com/bashsharp/bashsharp/engine/gosource"
)

func TestStory461Mdempsky16(t *testing.T) {
	path := filepath.Join(runtime.GOROOT(), "test", "typeparam", "mdempsky", "16.go")
	src, err := os.ReadFile(path)
	qt.Assert(t, qt.IsNil(err))
	out, stderr, err := runGoSource(t, "typeparam/mdempsky/16.go", string(src))
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, ""))
}

func TestStory461TypeParamAssertions(t *testing.T) {
	src := `package main
import "fmt"
type S struct{}
func (S) M() byte { return 0 }
type I[T any] interface{ M() T }
type J interface{ f(); g() }
func report[T, A any](x I[T]) {
	matched := false
	switch x.(type) {
	case A:
		matched = true
	}
	_, ok := x.(A)
	caught := ""
	func() {
		defer func() {
			if recover() != nil {
				caught = "panic"
			}
		}()
		_ = x.(A)
	}()
	fmt.Println(matched, ok, caught)
}
func nilAssert[T any]() bool {
	var x J
	_, ok := x.(T)
	return ok
}
func main() {
	report[byte, string](S{})
	report[byte, S](S{})
	fmt.Println(nilAssert[bool]())
}`
	out, stderr, err := runGoSource(t, "story461", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "false false panic\ntrue true \nfalse\n"))
}

// The rejection the evaluator no longer repeats still happens where Go
// performs it: go/types refuses an impossible concrete assertion at parse
// time, so no Go-source program reaches the evaluator with one. The classic
// dialect's runtime guard is covered by TestBashPPInterfaceDiagnostics
// ("assert impossible").
func TestStory461ImpossibleAssertionStillRejected(t *testing.T) {
	src := `package main
type I interface{ f(); g() }
func main() {
	var x I
	_, ok := x.(bool)
	_ = ok
}`
	_, err := gosource.Parse(strings.NewReader(src), "story461neg.go", gosource.Options{RunMain: true})
	qt.Assert(t, qt.IsNotNil(err))
	qt.Assert(t, qt.StringContains(err.Error(), "impossible type assertion"))
}

func TestStory461ReflectInterfaceResultAssertions(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want string
	}{
		{name: "bug510", src: `package main
import "reflect"
type S[T any] struct { a interface{} }
func (e S[T]) M() { v := reflect.ValueOf(e.a); _, _ = v.Interface().(int) }
func main() { S[int]{0}.M() }`, want: ""},
		{name: "issue47740b", src: `package main
import "reflect"
type S[T any] struct { a interface{} }
func (e S[T]) M() { v := reflect.ValueOf(e.a); _, _ = v.Interface().(int) }
func main() { e := S[int]{0}; e.M() }`, want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, stderr, err := runGoSource(t, "story461-"+tc.name, tc.src)
			qt.Assert(t, qt.IsNil(err), qt.Commentf("stdout=%q stderr=%q", out, stderr))
			qt.Assert(t, qt.Equals(out, tc.want))
			qt.Assert(t, qt.Equals(stderr, ""))
		})
	}
}

func TestStory461ReflectInterfaceResultAssertionOutsideCorpus(t *testing.T) {
	src := `package main
import "reflect"
func main() { _, ok := reflect.ValueOf(7).Interface().(int); if !ok { panic("authenticated dynamic type lost") } }`
	out, stderr, err := runGoSource(t, "story461-outside", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stdout=%q stderr=%q", out, stderr))
	qt.Assert(t, qt.Equals(out, ""))
	qt.Assert(t, qt.Equals(stderr, ""))
}

func TestStory461ReflectInterfaceResultAssertionMismatch(t *testing.T) {
	src := `package main
import "reflect"
func main() { _, ok := reflect.ValueOf(7).Interface().(string); if ok { panic("mismatched authenticated type accepted") } }`
	out, stderr, err := runGoSource(t, "story461-mismatch", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stdout=%q stderr=%q", out, stderr))
	qt.Assert(t, qt.Equals(out, ""))
	qt.Assert(t, qt.Equals(stderr, ""))
}

func TestStory461ImportedInterfacesCanBeEmbedded(t *testing.T) {
	src := `package main
import (
	"encoding"
	"fmt"
)
type Both interface { fmt.Stringer; encoding.BinaryMarshaler }
type Value string
func (Value) String() string { return "value" }
func (Value) MarshalBinary() ([]byte, error) { return []byte("value"), nil }
func main() {
	var both Both = Value("value")
	b, err := both.MarshalBinary()
	if err != nil || string(b) != "value" || both.String() != "value" { panic("embedded imported interface failed") }
}`
	out, stderr, err := runGoSource(t, "story461-imported-interface", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stdout=%q stderr=%q", out, stderr))
	qt.Assert(t, qt.Equals(out, ""))
	qt.Assert(t, qt.Equals(stderr, ""))
}
