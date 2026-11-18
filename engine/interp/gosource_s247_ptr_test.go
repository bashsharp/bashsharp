//go:build full

package interp_test

// Sprint: #247; Story: #672; Story-ID: fa5b3cf5a929

import (
	"strings"
	"testing"
)

// TestS247PtrNilConversions covers the typed nils issue15528 builds as
// interface elements: the integer constant 0 converted to unsafe.Pointer is
// the nil unsafe.Pointer, a nil pointer to an imported type keeps its dynamic
// type, and parentheses around a typed nil are transparent.
func TestS247PtrNilConversions(t *testing.T) {
	src := `package main
import (
	"fmt"
	"io"
	"os"
	"unsafe"
)
type Int int
func (i Int) String() string { return fmt.Sprintf("Int=%d", i) }
func (i Int) Strung() {}
type Strunger interface {
	fmt.Stringer
	Strung()
}
var elems = [...]struct{ x interface{} }{
	{unsafe.Pointer(uintptr(0))},
	{unsafe.Pointer(nil)},
	{io.Writer((*os.File)(nil))},
	{fmt.Stringer(Strunger(((*Int)(nil))))},
}
func main() {
	for _, e := range elems {
		fmt.Printf("%[1]T %[1]v\n", e.x)
	}
}`
	out, stderr, err := runGoSource(t, "s247-ptr-nil", src)
	if err != nil {
		t.Fatalf("run: %v\nstderr:\n%s", err, stderr)
	}
	want := "unsafe.Pointer <nil>\nunsafe.Pointer <nil>\n*os.File <nil>\n*main.Int <nil>\n"
	if out != want {
		t.Fatalf("out=%q want %q (stderr=%q)", out, want, stderr)
	}
}

// TestS247PtrForgedIntegerRefused is the negative control: no integer other
// than the constant 0 is trusted without a registered live pointer origin.
// A forged constant and a runtime uintptr refuse with a clear BASHPP error.
func TestS247PtrForgedIntegerRefused(t *testing.T) {
	cases := map[string]string{
		"constant": `package main
import "unsafe"
func main() { println(*(*int)(unsafe.Pointer(uintptr(0x1234)))) }`,
		"runtime": `package main
import "unsafe"
func main() {
	u := uintptr(4660)
	p := unsafe.Pointer(u)
	println(*(*int)(p))
}`,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			_, stderr, err := runGoSource(t, "s247-ptr-forged", src)
			if err == nil {
				t.Fatalf("forged address accepted; stderr=%q", stderr)
			}
			// A constant address may exist as an opaque forged value
			// (unsafebuiltins compares them) but never dereferences.
			if got := err.Error() + stderr; !strings.Contains(got, "BASHPP-EUNSAFE-ADDRESS") && !strings.Contains(got, "BASHPP-EUNSAFE-FORGED") {
				t.Fatalf("refusal=%q; want BASHPP-EUNSAFE-ADDRESS or BASHPP-EUNSAFE-FORGED", got)
			}
		})
	}
}
