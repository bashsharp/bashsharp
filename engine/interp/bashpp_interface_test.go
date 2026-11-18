// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestBashPPInterfaceAssignmentAssertionsAndTypeSwitch(t *testing.T) {
	const src = `type Count int
func (v Count) Show(prefix string) { echo "$prefix:$v"; }
func (v Count) Label(n int) string { return "$n:$v"; }
type Shower interface { Show(string) }
type Labeler interface { Label(int) string }
func main() {
	var v Count = 7
	var i Shower = v
	x, ok := i.(Count)
	echo "assert:$x:$ok"
	var l Labeler = v
	label := l.Label(3)
	echo "label:$label"
	i.Show(iface)
	y := i.(Count)
	y.Show(method)
	var nilI Shower
	switch n := nilI.(type) {
	case nil:
		echo nil-interface
	default:
		echo "$n"
	}
	var p *Count = nil
	var pi Shower = p
	switch q := pi.(type) {
	case nil:
		echo wrong
	default:
		echo typed-nil-pointer:${q-unset}
	}
	switch z := i.(type) {
	case nil:
		echo wrong
	case Count:
		echo "type:$z"
	default:
		echo wrong
	}
	snap(i)
}
func snap(i Shower) {
	a, ok := i.(Count)
	echo "subshell:$a:$ok"
}
main()
`
	for _, bytewise := range []bool{false, true} {
		var rd interface{ Read([]byte) (int, error) } = strings.NewReader(src)
		if bytewise {
			rd = iotest.OneByteReader(strings.NewReader(src))
		}
		f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(rd, "iface.bpp")
		if err != nil {
			t.Fatalf("bytewise=%v: parse: %v", bytewise, err)
		}
		var out strings.Builder
		r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
		err = r.Run(context.Background(), f)
		if err != nil {
			t.Fatalf("bytewise=%v: err=%v output=%q", bytewise, err, out.String())
		}
		want := "assert:7:true\nlabel:3:7\niface:7\nmethod:7\nnil-interface\ntyped-nil-pointer:\ntype:7\nsubshell:7:true\n"
		if out.String() != want {
			t.Fatalf("bytewise=%v: output = %q, want %q", bytewise, out.String(), want)
		}
	}
}

func TestBashPPInterfaceDiagnostics(t *testing.T) {
	tests := []struct{ name, src, want string }{
		{"duplicate", "type I interface { M(int) string M(int) string }\n", "BASHPP-EINTERFACE-DUPLICATE: interface I declares method M more than once\n"},
		{"missing", "type T int\nfunc (v T) N(s string) { }\ntype I interface { M(string) }\nfunc main() { var v T = 1; var i I = v }\nmain()\n", "BASHPP-EINTERFACE-MISSING: T does not implement interface (missing method M)\n"},
		{"pointer receiver not in value method set", "type T int\nfunc (p *T) M() { }\ntype I interface { M() }\nfunc main() { var v T = 1; var i I = v }\nmain()\n", "BASHPP-EINTERFACE-MISSING: T does not implement interface (missing method M)\n"},
		{"wrong signature", "type T int\nfunc (v T) M(n int) { }\ntype I interface { M(string) }\nfunc main() { var v T = 1; var i I = v }\nmain()\n", "BASHPP-EINTERFACE-SIGNATURE: T method M has wrong signature\n"},
		{"embedded promoted missing", "type T int\nfunc (v T) Close() { }\ntype Reader interface { Read(string) }\ntype Closer interface { Close() }\ntype ReadCloser interface { Reader; Closer }\nfunc main() {\n var v T = 1\n var i ReadCloser = v\n}\nmain()\n", "BASHPP-EINTERFACE-MISSING: T does not implement interface (missing method Read)\n"},
		{"embedded duplicate identical accepted conflict later", "type A interface { M(int) }\ntype B interface { M(string) }\ntype C interface { A; B }\n", "BASHPP-EINTERFACE-CONFLICT: interface C has conflicting method M\n"},
		// `interface { T }` with T a declared type is a one-term type set,
		// as in Go; only a name the runtime cannot resolve is an error.
		{"embedded unresolved", "type I interface { Unknown }\n", "BASHPP-EINTERFACE-EMBED: interface I embeds non-interface Unknown\n"},
		{"assert fail", "type T int\nfunc (v T) M(s string) { }\ntype U int\nfunc (v U) M(s string) { }\ntype I interface { M(string) }\nfunc main() { var v T = 1; var i I = v; x := i.(U); echo $x }\nmain()\n", "BASHPP-EASSERT-FAIL: interface value has dynamic type T, not U\n"},
		{"assert impossible", "type T int\nfunc (v T) M(s string) { }\ntype U int\ntype I interface { M(string) }\nfunc main() { var v T = 1; var i I = v; x, ok := i.(U); echo $x $ok }\nmain()\n", "BASHPP-EASSERT-IMPOSSIBLE: U cannot be asserted from I\n"},
		{"nil interface call", "type T int\nfunc (v T) M() { }\ntype I interface { M() }\nfunc main() {\n var i I\n i.M()\n}\nmain()\n", "nil interface has no method M\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(tc.src), "badiface.bpp")
			if err != nil {
				t.Fatal(err)
			}
			var out strings.Builder
			r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
			err = r.Run(context.Background(), f)
			var status interp.ExitStatus
			if !errors.As(err, &status) || status != 2 || out.String() != tc.want {
				t.Fatalf("err/output = %v/%q, want status 2/%q", err, out.String(), tc.want)
			}
		})
	}
}

func TestBashPPInterfaceValueCopyAndAssertionZero(t *testing.T) {
	const src = `type Box struct { N int }
func (v Box) Show(prefix string) { echo "$prefix:${v.N}"; }
type Shower interface { Show(string) }
type Other int
func (v Other) Show(prefix string) { echo "$prefix:$v"; }
func main() {
	var box Box = Box{N: 1}
	var i Shower = box
	box.N = 9
	stored := i.(Box)
	printf 'copy:%s\n' stored.N
	zero, ok := i.(Other)
	echo "zero:$zero:$ok"
	p := &box
	var pi Shower = p
	q := pi.(*Box)
	q.N = 12
	printf 'pointer:%s\n' box.N
}
main()
`
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "ifacecopy.bpp")
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
	if err := r.Run(context.Background(), f); err != nil {
		t.Fatalf("err=%v output=%q", err, out.String())
	}
	if want := "copy:1\nzero:0:false\npointer:12\n"; out.String() != want {
		t.Fatalf("output = %q, want %q", out.String(), want)
	}
}

func TestBashPPInterfaceMethodSetsAndPointerIdentity(t *testing.T) {
	const src = `type Box struct { N int }
func (v Box) Value(prefix string) { printf '%s:%s\n' "$prefix" v.N; }
func (p *Box) Set(n int) { p.N = n; }
type Valuer interface { Value(string) }
type Mutator interface { Set(int) }
func main() {
	var b Box = Box{N: 4}
	var v Valuer = b
	b.N = 9
	v.Value(copy)
	bp := &b
	var m Mutator = bp
	m.Set(12)
	printf 'mutated:%s\n' b.N
}
main()
`
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "ifacemethods.bpp")
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
	if err := r.Run(context.Background(), f); err != nil {
		t.Fatalf("err=%v output=%q", err, out.String())
	}
	if want := "copy:4\nmutated:12\n"; out.String() != want {
		t.Fatalf("output = %q, want %q", out.String(), want)
	}
}

func TestBashPPEmbeddedInterfacePromotionAndAssertions(t *testing.T) {
	const src = `type File struct { N int }
func (v File) Read(prefix string) { echo "read:$prefix"; }
func (p *File) Close() { p.N = 0; echo closed; }
type Reader interface { Read(string) }
type Closer interface { Close() }
type ReadCloser interface { Reader; Closer }
func main() {
	var f File = File{N: 8}
	p := &f
	var rc ReadCloser = p
	rc.Read(file)
	rc.Close()
	var r Reader = rc
	r.Read(after)
	c, ok := r.(Closer)
	echo "assert-interface:$ok"
	c.Close()
	switch x := r.(type) {
	case Closer:
		echo closer
	default:
		echo "$x"
	}
	var nilRC ReadCloser
	var nilR Reader = nilRC
	nilC, nilOK := nilR.(Closer)
	echo "nil-interface-assert:$nilC:$nilOK"
}
main()
`
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "embediface.bpp")
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
	if err := r.Run(context.Background(), f); err != nil {
		t.Fatalf("err=%v output=%q", err, out.String())
	}
	want := "read:file\nclosed\nread:after\nassert-interface:true\nclosed\ncloser\nnil-interface-assert::false\n"
	if out.String() != want {
		t.Fatalf("output = %q, want %q", out.String(), want)
	}
}

func TestBashPPInterfaceReadonlyMutationThroughAlias(t *testing.T) {
	const src = `type Box struct { N int }
func (p *Box) Set(n int) { p.N = n; }
type Mutator interface { Set(int) }
func main() {
	var b Box = Box{N: 1}
	alias := &b
	var m Mutator = alias
	readonly b
	m.Set(2)
	printf 'still:%s\n' b.N
}
main()
`
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "ifacereadonly.bpp")
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
	err = r.Run(context.Background(), f)
	if err != nil || out.String() != "BASHPP-EREADONLY-MUTATION: cannot mutate readonly value \"b\" through pointer\nstill:1\n" {
		t.Fatalf("err/output = %v/%q, want readonly pointer mutation blocked with value intact", err, out.String())
	}
}

func TestBashPPTypedNilInterfaceMethodCalls(t *testing.T) {
	const src = `type T int
func (p *T) Pointer() { echo pointer-nil; }
func (v T) Value() { echo value; }
type PointerCaller interface { Pointer() }
type ValueCaller interface { Value() }
func main() {
	var p *T = nil
	var pi PointerCaller = p
	pi.Pointer()
	var vi ValueCaller = p
	vi.Value()
}
main()
`
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "ifacetypednil.bpp")
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
	err = r.Run(context.Background(), f)
	var status interp.ExitStatus
	if !errors.As(err, &status) || status != 2 || out.String() != "pointer-nil\nvalue method Value called using nil *T pointer\n" {
		t.Fatalf("err/output = %v/%q, want status 2 and typed-nil value-method diagnostic", err, out.String())
	}
}
