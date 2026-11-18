// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp_test

import (
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/go-quicktest/qt"
)

func TestBashPPPromotedFieldsReadWriteAndShadowing(t *testing.T) {
	const src = `type Leaf struct { N int }
type Left struct { Leaf }
type Right struct { N int }
type Outer struct { Left; Label string }
type Shadow struct { Left; N int }
func main() {
 o := Outer{Left: Left{Leaf: Leaf{N: 3}}, Label: "x"}
 printf '%s:%s:' o.N o.Label
 o.N = 4
 s := Shadow{Left: Left{Leaf: Leaf{N: 5}}, N: 6}
 printf '%s:%s' o.N s.N
}
main()
`
	out, stderr, err := runBashSharpCall(t, src)
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(out, "3:x:4:6"))
}

func TestBashPPPromotedPointerFieldAndMethods(t *testing.T) {
	const src = `type Leaf struct { N int }
func (v Leaf) Value() { printf 'v%s:' v.N }
func (v Leaf) Change() { v.N = 99 }
func (p *Leaf) Set(n int) { p.N = n }
type Outer struct { *Leaf }
func main() {
 p := new(Leaf)
 p.N = 2
 o := Outer{Leaf: p}
 o.Value()
 o.Change()
 printf '%s:' o.N
 o.Set(7)
 printf '%s:' o.N
 f := o.Value
 f()
}
main()
`
	out, stderr, err := runBashSharpCall(t, src)
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(out, "v2:2:7:v7:"))
}

func TestBashPPPromotedMethodSets(t *testing.T) {
	const src = `type Leaf struct { N int }
func (v Leaf) Value() { }
func (p *Leaf) Set(n int) { p.N = n }
type ValueOuter struct { Leaf }
type PointerOuter struct { *Leaf }
type Valuer interface { Value() }
type Setter interface { Set(int) }
func main() {
 var v ValueOuter
 var vv Valuer = v
 p := &v
 var ps Setter = p
 q := new(Leaf)
 w := PointerOuter{Leaf: q}
 var ws Setter = w
 vv.Value()
 ps.Set(4)
 ws.Set(5)
 printf '%s:%s' v.N w.N
}
main()
`
	out, stderr, err := runBashSharpCall(t, src)
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(out, "4:5"))
}

func TestBashPPPromotedPointerMethodCallsAndExpressions(t *testing.T) {
	const src = `type Leaf struct { N int }
func (p *Leaf) Set(n int) { p.N = n }
type ValueOuter struct { Leaf }
type PointerOuter struct { *Leaf }
func main() {
 var v ValueOuter
 v.Set(8)
 p := &v
 (*ValueOuter).Set(p, 9)
 q := new(Leaf)
 w := PointerOuter{Leaf: q}
 PointerOuter.Set(w, 10)
 printf '%s:%s' v.N w.N
}
main()
`
	out, stderr, err := runBashSharpCall(t, src)
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(out, "9:10"))
}

func TestBashPPValueEmbeddingExcludesPointerMethodFromValueSet(t *testing.T) {
	const src = `type Leaf struct { N int }
func (p *Leaf) Set(n int) { p.N = n }
type Outer struct { Leaf }
type Setter interface { Set(int) }
func main() {
 var o Outer
 var s Setter = o
}
main()
`
	_, stderr, err := runBashSharpCall(t, src)
	qt.Assert(t, qt.ErrorIs(err, interp.ExitStatus(2)))
	qt.Assert(t, qt.StringContains(stderr, "missing method Set"))
}

func TestBashPPEmbeddedAliasPromotesTargetMethods(t *testing.T) {
	const src = `type Leaf struct { N int }
func (p *Leaf) Set(n int) { p.N = n }
type Alias = Leaf
type Outer struct { Alias }
func main() {
 var o Outer
 o.Set(12)
 printf '%s' o.N
}
main()
`
	out, stderr, err := runBashSharpCall(t, src)
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(out, "12"))
}

func TestBashPPPromotedSelectorAmbiguity(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
	}{
		{"field", `type A struct { X int }
type B struct { X int }
type C struct { A; B }
func main() {
 var c C
 x := c.X
 printf '%s' "$x"
}
main()
`},
		{"method", `type A int
func (v A) M() { }
type B int
func (v B) M() { }
type C struct { A; B }
func main() {
 var c C
 c.M()
}
main()
`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, stderr, err := runBashSharpCall(t, tc.src)
			qt.Assert(t, qt.ErrorIs(err, interp.ExitStatus(2)))
			qt.Assert(t, qt.StringContains(stderr, "BASHPP-ESELECTOR-AMBIGUOUS"))
		})
	}
}

func TestBashPPPromotedNilPointerDiagnostic(t *testing.T) {
	const src = `type Leaf struct { N int }
type Outer struct { *Leaf }
func main() {
 var o Outer
 x := o.N
 printf '%s' "$x"
}
main()
`
	_, stderr, err := runBashSharpCall(t, src)
	qt.Assert(t, qt.ErrorIs(err, interp.ExitStatus(2)))
	qt.Assert(t, qt.StringContains(stderr, "BASHPP-ENIL-DEREF"))
}

func TestBashPPEmbeddedInterfacePromotesDynamicMethods(t *testing.T) {
	const src = `type Speaker interface { Speak() }
type Voice struct { N int }
func (v Voice) Speak() { printf 'v%s:' v.N }
type Outer struct { Speaker }
type Alias = Speaker
type AliasOuter struct { Alias }
func main() {
 v := Voice{N: 3}
 var s Speaker = v
 o := Outer{Speaker: s}
 o.Speak()
 var promoted Speaker = o
 promoted.Speak()
 selected := o.Speaker
 selected.Speak()
 o.Speaker.Speak()
 a := AliasOuter{Alias: s}
 a.Speak()
 p := &o
 p.Speak()
 var pointerPromoted Speaker = p
 pointerPromoted.Speak()
 Outer.Speak(o)
 (*Outer).Speak(p)
 f := o.Speak
 f()
}
main()
`
	out, stderr, err := runBashSharpCall(t, src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""), qt.Commentf("stdout: %s", out))
	qt.Assert(t, qt.Equals(out, "v3:v3:v3:v3:v3:v3:v3:v3:v3:v3:"))
}

func TestBashPPEmbeddedInterfaceNilAndAmbiguity(t *testing.T) {
	tests := []struct{ name, src, want string }{
		{"nil", `type Speaker interface { Speak() }
type Outer struct { Speaker }
func main() {
 var o Outer
 o.Speaker.Speak()
}
main()
`, "nil interface has no method Speak"},
		{"ambiguous", `type Speaker interface { Speak() }
type Left struct { Speaker }
type Right struct { Speaker }
type Outer struct { Left; Right }
func main() {
 var o Outer
 o.Speak()
}
main()
`, "BASHPP-ESELECTOR-AMBIGUOUS"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, stderr, err := runBashSharpCall(t, test.src)
			qt.Assert(t, qt.ErrorIs(err, interp.ExitStatus(2)))
			qt.Assert(t, qt.StringContains(stderr, test.want))
		})
	}
}

func TestBashPPEmbeddedInterfaceValueAndReferenceSemantics(t *testing.T) {
	const src = `type Speaker interface { Speak() }
type Voice struct { N int }
func (v Voice) Speak() { printf '%s:' v.N }
type Outer struct { Speaker }
func main() {
 v := Voice{N: 1}
 var value Speaker = v
 h := Outer{Speaker: value}
 v.N = 2
 h.Speak()

 p := new(Voice)
 p.N = 3
 var reference Speaker = p
 h.Speaker = reference
 copied := h
 p.N = 4
 h.Speak()
 copied.Speak()

 h.Speaker = value
 copied.Speak()
 h.Speak()
}
main()
`
	out, stderr, err := runBashSharpCall(t, src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "1:4:4:4:1:"))
}

func TestBashPPEmbeddedInterfaceDirectMethodShadowsAndEmbeddedInterfacesCompose(t *testing.T) {
	const src = `type Reader interface { Read() }
type Writer interface { Write() }
type ReadWriter interface { Reader; Writer }
type Device int
func (d Device) Read() { printf r }
func (d Device) Write() { printf w }
type Outer struct { ReadWriter }
func (o Outer) Read() { printf R }
func main() {
	var device Device = 0
	var d ReadWriter = device
 o := Outer{ReadWriter: d}
 o.Read()
 o.Write()
}
main()
`
	out, stderr, err := runBashSharpCall(t, src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "Rw"))
}

func TestBashPPEmbeddedInterfaceTypedNilRetainsDynamicType(t *testing.T) {
	const src = `type Speaker interface { Speak() }
type Voice int
func (p *Voice) Speak() { printf nil-pointer }
type Outer struct { Speaker }
func main() {
 var p *Voice
 var s Speaker = p
 o := Outer{Speaker: s}
 o.Speaker.Speak()
}
main()
`
	out, stderr, err := runBashSharpCall(t, src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "nil-pointer"))
}

func TestBashPPEmbeddedInterfaceTypedNilDereferenceDiagnostic(t *testing.T) {
	const src = `type Speaker interface { Speak() }
type Voice struct { N int }
func (p *Voice) Speak() {
 x := p.N
 println(x)
}
type Outer struct { Speaker }
func main() {
 var p *Voice
 var s Speaker = p
 o := Outer{Speaker: s}
 o.Speaker.Speak()
}
main()
`
	_, stderr, err := runBashSharpCall(t, src)
	qt.Assert(t, qt.ErrorIs(err, interp.ExitStatus(2)))
	qt.Assert(t, qt.StringContains(stderr, "BASHPP-ENIL-DEREF"))
}

func TestBashPPPointerToInterfaceEmbeddingRejected(t *testing.T) {
	const src = `type Speaker interface { Speak() }
type Outer struct { *Speaker }
func main() {
 var o Outer
}
main()
`
	_, stderr, err := runBashSharpCall(t, src)
	qt.Assert(t, qt.ErrorIs(err, interp.ExitStatus(2)))
	qt.Assert(t, qt.StringContains(stderr, "BASHPP-ESTRUCT-EMBED: pointer to interface type *Speaker cannot be embedded"))
}

func TestBashPPAliasPointerEmbeddingPromotesFieldsMethodsAndInterface(t *testing.T) {
	const src = `type Leaf struct { N int }
func (v Leaf) Value() { printf 'v%s:' v.N }
func (p *Leaf) Set(n int) { p.N = n }
type LeafPtr = *Leaf
type Outer struct { LeafPtr }
type Setter interface { Set(int) }
func main() {
 p := new(Leaf)
 o := Outer{LeafPtr: p}
 o.N = 3
 o.Value()
 o.Set(4)
 var s Setter = o
 s.Set(5)
 printf '%s' o.N
}
main()
`
	out, stderr, err := runBashSharpCall(t, src)
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(out, "v3:5"))
}

func TestBashPPAliasPointerEmbeddingNilDiagnostic(t *testing.T) {
	const src = `type Leaf struct { N int }
type LeafPtr = *Leaf
type Outer struct { LeafPtr }
func main() {
 var o Outer
 x := o.N
 printf '%s' "$x"
}
main()
`
	_, stderr, err := runBashSharpCall(t, src)
	qt.Assert(t, qt.ErrorIs(err, interp.ExitStatus(2)))
	qt.Assert(t, qt.StringContains(stderr, "BASHPP-ENIL-DEREF"))
}

func TestBashPPDefinedPointerEmbeddingRejected(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want string
	}{
		{"direct", `type Leaf struct { N int }
type LeafPtr *Leaf
type Outer struct { LeafPtr }
func main() { var o Outer }
main()
`, "LeafPtr"},
		{"through alias", `type Leaf struct { N int }
type Defined *Leaf
type Alias = Defined
type Outer struct { Alias }
func main() { var o Outer }
main()
`, "Alias"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, stderr, err := runBashSharpCall(t, tc.src)
			qt.Assert(t, qt.ErrorIs(err, interp.ExitStatus(2)))
			qt.Assert(t, qt.StringContains(stderr, "BASHPP-ESTRUCT-EMBED: defined pointer type "+tc.want+" cannot be embedded"))
		})
	}
}
