//go:build full

package interp_test

import (
	"runtime"
	"strings"
	"testing"

	"github.com/go-quicktest/qt"
)

func TestS243UnsafeBlankStructView(t *testing.T) {
	const prefix = `package main
import "unsafe"
type Source struct { x, y, z int }
type Blank struct { _, _, _ int }
type Nested struct { _ struct { a, b, c int } }
`
	// arm64 shares the 64-bit little-endian gc layout; since the struct
	// overlay support (Sprint 374) the same cases hold there.
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		_, stderr, err := runGoSource(t, "s243-unsafe-architecture", prefix+`func main() { _ = *(*Blank)(unsafe.Pointer(&Source{})) }`)
		qt.Assert(t, qt.IsNotNil(err))
		qt.Assert(t, qt.IsTrue(strings.Contains(stderr, "BASHPP-EUNSAFE-LAYOUT")), qt.Commentf("stderr: %s", stderr))
		return
	}
	t.Run("blank field comparison", func(t *testing.T) {
		src := prefix + `
func blank(x, y, z int) Blank { return *(*Blank)(unsafe.Pointer(&Source{x, y, z})) }
func nested(x, y, z int) any { return *(*Nested)(unsafe.Pointer(&Source{x, y, z})) }
func main() {
	if blank(1, 2, 3) != blank(4, 5, 6) { panic("blank bytes compared") }
	if nested(1, 2, 3) != nested(4, 5, 6) { panic("nested blank bytes compared") }
}
`
		out, stderr, err := runGoSource(t, "s243-unsafe-blank", src)
		qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
		qt.Assert(t, qt.Equals(out, ""))
		qt.Assert(t, qt.Equals(stderr, ""))
	})

	t.Run("identity escaped storage and typed nil", func(t *testing.T) {
		src := prefix + `
func escaped() *Blank { p := &Source{1, 2, 3}; return (*Blank)(unsafe.Pointer(p)) }
func main() {
 p := &Source{1, 2, 3}
 a := (*Blank)(unsafe.Pointer(p))
 b := (*Blank)(unsafe.Pointer(p))
 if a != b { panic("same allocation lost identity") }
 q := &Source{1, 2, 3}
 if a == (*Blank)(unsafe.Pointer(q)) { panic("distinct allocations collapsed") }
 retained := escaped()
 if *retained != (Blank{}) { panic("escaped pointer lost storage") }
 var nilSource *Source
 nilBlank := (*Blank)(unsafe.Pointer(nilSource))
 if nilBlank != nil { panic("typed nil became nonnil") }
 var boxed any = nilBlank
 if boxed == nil { panic("typed nil lost dynamic type") }
}
`
		out, stderr, err := runGoSource(t, "s243-unsafe-identity", src)
		qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
		qt.Assert(t, qt.Equals(out, ""))
		qt.Assert(t, qt.Equals(stderr, ""))
	})

	t.Run("receive through view", func(t *testing.T) {
		src := strings.Replace(prefix, `import "unsafe"`, `import ("fmt"; "unsafe")`, 1) + `
func main() {
	p := &Source{1, 2, 3}
	v := (*Blank)(unsafe.Pointer(p))
	c := make(chan Blank, 1)
	c <- Blank{}
	select { case *v = <-c: }
	fmt.Println(*v == (Blank{}), *p)
}`
		differGoSource(t, src, nil, "")
	})

	for _, tc := range []struct{ name, target, want string }{
		{"write through view", `func main() { p := &Source{1, 2, 3}; v := (*Blank)(unsafe.Pointer(p)); *v = Blank{} }`, "BASHPP-EUNSAFE-WRITE: writes through reinterpreted blank views cannot preserve blank-field bytes"},
		{"observable target", `type Bad struct { x, _, _ int }; var _ = *(*Bad)(unsafe.Pointer(&Source{}))`, "BASHPP-EUNSAFE-VIEW: target Bad mixes observable fields with blank-field bytes that cannot be represented"},
		{"trailing zero field padding", `type Padded struct { x int; z struct{} }; type Small struct { _ int }; var _ = *(*Small)(unsafe.Pointer(&Padded{}))`, "BASHPP-EUNSAFE-LAYOUT"},
		{"layout mismatch", `type Bad struct { _, _ int }; var _ = *(*Bad)(unsafe.Pointer(&Source{}))`, "BASHPP-EUNSAFE-LAYOUT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := prefix + tc.target + "\n"
			if !strings.Contains(tc.target, "func main") {
				src += "func main() {}\n"
			}
			_, stderr, err := runGoSource(t, "s243-unsafe-negative", src)
			qt.Assert(t, qt.IsNotNil(err))
			qt.Assert(t, qt.IsTrue(strings.Contains(stderr, tc.want)), qt.Commentf("stderr: %s", stderr))
		})
	}
}
