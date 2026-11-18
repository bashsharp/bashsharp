//go:build full

package interp_test

import "testing"

// Sprint: #247; Story: #673
//
// Upstream roots whose assertions are gc toolchain or memory-layout
// observations that Bash#'s interpreted mode deliberately does not mimic
// (bashsharp docs/bashpp-go-implementation-claim.md, "Language, not
// implementation"; sh docs/bashpp-compiler-artifact-contracts.md, Sprint 247
// section). Those roots stay FAIL by ID in interpreted mode. These
// independent programs pin the language behaviour each root also touches,
// compared across unchanged Go, interpreted Bash# and the compiled artifact.
func TestS247DecisionReplacementConformance(t *testing.T) {
	for name, source := range map[string]string{
		// fixedbugs/issue20014.go: `go:"track"` fields are reported by the
		// linker (-ldflags -k=main.fieldTrackInfo) under -goexperiment
		// fieldtrack. Without the experiment the tag is an ordinary struct
		// tag, and tracked fields read and write like any other.
		"struct-tags-and-tracked-fields": `package main
import (
	"fmt"
	"reflect"
)
type T struct {
	X int ` + "`go:\"track\"`" + `
	Y int ` + "`go:\"track\"`" + `
	Z int
}
func (t *T) GetX() int { return t.X }
func (t *T) GetY() int { return t.Y }
func main() {
	t := &T{X: 1, Y: 2, Z: 3}
	t.Y += t.GetX()
	typ := reflect.TypeOf(*t)
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		fmt.Printf("%s %q ", f.Name, f.Tag.Get("go"))
	}
	fmt.Println(t.GetX(), t.GetY(), t.Z)
}
`,
		// fixedbugs/issue30862.go: //go:nointerface only exists under the
		// fieldtrack experiment; without it, a pointer method promoted through
		// an embedded struct satisfies interfaces through both the pointer and
		// the embedding pointer, but not through the embedding value.
		"promoted-embedded-pointer-method-sets": `package main
import "fmt"
type NoitfStruct struct{ F, G int }
func (t *NoitfStruct) NoInterfaceMethod() {}
type EmbedImported struct{ NoitfStruct }
type EmbedPointer struct{ *NoitfStruct }
type M interface{ NoInterfaceMethod() }
func main() {
	_, a := interface{}(new(NoitfStruct)).(M)
	_, b := interface{}(new(EmbedImported)).(M)
	_, c := interface{}(EmbedImported{}).(M)
	_, d := interface{}(EmbedPointer{&NoitfStruct{}}).(M)
	fmt.Println(a, b, c, d)
}
`,
		// strcopy.go: the root compares string data addresses through
		// reflect.StringHeader. Whether equal strings share memory is not
		// observable in the language; that a string converted from bytes is
		// independent of them, and compares by content, is.
		"string-conversion-copies": `package main
import "fmt"
func main() {
	buf := make([]byte, 2<<10)
	for i := range buf {
		buf[i] = byte('a' + i%26)
	}
	large := string(buf)
	sub := large[10:12]
	b := []byte(sub)
	subcopy := string(b)
	b[0] = 'Z'
	buf[10] = 'Y'
	fmt.Println(sub, subcopy, string(b), large[10:12], sub == subcopy, len(large))
}
`,
		// fixedbugs/issue8606b.go: the root points string data at an
		// inaccessible mmap page and relies on gc's comparison order never
		// reading it. The language contract is content equality of struct
		// and interface values with string fields.
		"struct-string-interface-equality": `package main
import "fmt"
type SI struct {
	s string
	i int
}
type SS struct {
	s string
	t string
}
func main() {
	for _, test := range []struct{ a, b interface{} }{
		{SI{s: "foo", i: 1}, SI{s: "foo", i: 2}},
		{SS{s: "foo", t: "a"}, SS{s: "foo", t: "aa"}},
		{SS{s: "a", t: "foo"}, SS{s: "b", t: "foo"}},
		{SS{s: "x", t: "y"}, SS{s: "x", t: "y"}},
		{SI{s: "ab", i: 3}, SI{s: string([]byte{'a', 'b'}), i: 3}},
	} {
		fmt.Print(test.a == test.b, " ")
	}
	fmt.Println()
}
`,
	} {
		t.Run(name, func(t *testing.T) { typedSendThreeModes(t, source) })
	}
}
