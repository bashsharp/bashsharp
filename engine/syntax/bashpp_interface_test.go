// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package syntax

import (
	"bytes"
	"io"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
)

const bashppInterfaceFixture = `type Shower interface { Show(int) string; Ptr() int }
func f() {
	x, ok := i.(Count)
	switch v := i.(type) {
	case nil:
		echo nil
	case Count:
		echo "$v"
	default:
		echo other
	}
}
`

func parseBashPPInterface(t *testing.T, rd io.Reader) *File {
	t.Helper()
	f, err := NewParser(Variant(LangBashPP)).Parse(rd, "interface.bpp")
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestBashPPInterfaceASTStreamingWalkAndPrint(t *testing.T) {
	buffered := parseBashPPInterface(t, strings.NewReader(bashppInterfaceFixture))
	streamed := parseBashPPInterface(t, iotest.OneByteReader(strings.NewReader(bashppInterfaceFixture)))
	if !reflect.DeepEqual(buffered, streamed) {
		t.Fatalf("buffered and one-byte trees differ:\n%#v\n%#v", buffered, streamed)
	}
	decl := buffered.Stmts[0].Cmd.(*BashPPDecl)
	iface, ok := decl.DeclTypeExpr.(*BashPPInterfaceType)
	if !ok || len(iface.Methods) != 2 || iface.Methods[0].Name.Value != "Show" || iface.Methods[1].Name.Value != "Ptr" {
		t.Fatalf("interface type = %#v", decl.DeclTypeExpr)
	}
	fn := buffered.Stmts[1].Cmd.(*BashPPFuncDecl)
	if _, ok := fn.Body.Stmts[0].Cmd.(*BashPPShortDecl).Expr.(*BashPPTypeAssertExpr); !ok {
		t.Fatalf("type assertion = %#v", fn.Body.Stmts[0].Cmd)
	}
	sw := fn.Body.Stmts[1].Cmd.(*BashPPSwitch)
	if !sw.TypeSwitch || len(sw.Arms) != 3 {
		t.Fatalf("type switch = %#v", sw)
	}
	seen := map[string]bool{}
	Walk(buffered, func(n Node) bool {
		if n != nil {
			seen[reflect.TypeOf(n).String()] = true
		}
		return true
	})
	for _, want := range []string{"*syntax.BashPPInterfaceType", "*syntax.BashPPMethodSpec", "*syntax.BashPPTypeAssertExpr"} {
		if !seen[want] {
			t.Fatalf("Walk missed %s in %v", want, seen)
		}
	}
	var printed bytes.Buffer
	if err := NewPrinter().Print(&printed, buffered); err != nil {
		t.Fatal(err)
	}
	if printed.String() != bashppInterfaceFixture {
		t.Fatalf("print = %q, want %q", printed.String(), bashppInterfaceFixture)
	}
	reparsed := parseBashPPInterface(t, strings.NewReader(printed.String()))
	if !reflect.DeepEqual(buffered, reparsed) {
		t.Fatal("parse/print/reparse changed interface tree")
	}
}

func TestBashPPInterfaceMethodSpecSeparators(t *testing.T) {
	const canonical = "type I interface { A(); B(int) string }\n"
	for _, src := range []string{
		canonical,
		"type I interface {\n A()\n B(int) string\n}\n",
	} {
		buffered := parseBashPPInterface(t, strings.NewReader(src))
		streamed := parseBashPPInterface(t, iotest.OneByteReader(strings.NewReader(src)))
		if !reflect.DeepEqual(buffered, streamed) {
			t.Fatalf("buffered and one-byte trees differ for %q", src)
		}
		var printed bytes.Buffer
		if err := NewPrinter().Print(&printed, buffered); err != nil {
			t.Fatal(err)
		}
		if printed.String() != canonical {
			t.Fatalf("print = %q, want canonical %q", printed.String(), canonical)
		}
		_ = parseBashPPInterface(t, strings.NewReader(printed.String()))
	}
}

func TestBashPPEmbeddedInterfaceASTStreamingWalkAndPrint(t *testing.T) {
	const src = `type Reader interface { Read(string) int }
type Writer interface { Write(string) int }
type ReadWriter interface { Reader; Reset(); Writer }
`
	buffered := parseBashPPInterface(t, strings.NewReader(src))
	streamed := parseBashPPInterface(t, iotest.OneByteReader(strings.NewReader(src)))
	if !reflect.DeepEqual(buffered, streamed) {
		t.Fatalf("buffered and one-byte trees differ:\n%#v\n%#v", buffered, streamed)
	}
	decl := buffered.Stmts[2].Cmd.(*BashPPDecl)
	iface, ok := decl.DeclTypeExpr.(*BashPPInterfaceType)
	if !ok || len(iface.Elems) != 3 || len(iface.Methods) != 1 {
		t.Fatalf("embedded interface type = %#v", decl.DeclTypeExpr)
	}
	if _, ok := iface.Elems[0].Embedded.(*BashPPNamedType); !ok {
		t.Fatalf("first element is not embedded named interface: %#v", iface.Elems[0])
	}
	if iface.Elems[1].Method == nil || iface.Elems[1].Method.Name.Value != "Reset" {
		t.Fatalf("second element is not a direct method: %#v", iface.Elems[1])
	}
	if _, ok := iface.Elems[2].Embedded.(*BashPPNamedType); !ok {
		t.Fatalf("third element is not embedded named interface: %#v", iface.Elems[2])
	}
	seen := map[string]bool{}
	Walk(buffered, func(n Node) bool {
		if n != nil {
			seen[reflect.TypeOf(n).String()] = true
		}
		return true
	})
	for _, want := range []string{"*syntax.BashPPInterfaceElem", "*syntax.BashPPNamedType", "*syntax.BashPPMethodSpec"} {
		if !seen[want] {
			t.Fatalf("Walk missed %s in %v", want, seen)
		}
	}
	var printed bytes.Buffer
	if err := NewPrinter().Print(&printed, buffered); err != nil {
		t.Fatal(err)
	}
	if printed.String() != src {
		t.Fatalf("print = %q, want %q", printed.String(), src)
	}
	reparsed := parseBashPPInterface(t, strings.NewReader(printed.String()))
	if !reflect.DeepEqual(buffered, reparsed) {
		t.Fatal("parse/print/reparse changed embedded interface tree")
	}
}

func TestBashPPEmbeddedInterfaceNamesAreNotMethodShorthand(t *testing.T) {
	const src = `type Reader interface { Read(string) int }
type Closer interface { Close() }
type ReadCloser interface { Reader; Closer }
`
	f := parseBashPPInterface(t, strings.NewReader(src))
	iface := f.Stmts[2].Cmd.(*BashPPDecl).DeclTypeExpr.(*BashPPInterfaceType)
	if len(iface.Elems) != 2 || len(iface.Methods) != 0 {
		t.Fatalf("interface elements = %#v; methods = %#v", iface.Elems, iface.Methods)
	}
	for i, elem := range iface.Elems {
		if _, ok := elem.Embedded.(*BashPPNamedType); !ok || elem.Method != nil {
			t.Fatalf("element %d parsed as %#v", i, elem)
		}
	}
	var printed bytes.Buffer
	if err := NewPrinter().Print(&printed, f); err != nil {
		t.Fatal(err)
	}
	if printed.String() != src {
		t.Fatalf("print = %q, want %q", printed.String(), src)
	}
}

func TestBashPPInterfaceFallbackBoundaries(t *testing.T) {
	for _, src := range []string{
		"type I interface { M(int) string }\n",
		"func f() {\n\tx, ok := i.(T)\n}\n",
		"func f() {\n\tswitch v := i.(type) { default: echo d }\n}\n",
	} {
		for _, lang := range []LangVariant{LangBash, LangPOSIX} {
			f, _ := NewParser(Variant(lang), RecoverErrors(8)).Parse(strings.NewReader(src), "")
			if f != nil {
				Walk(f, func(n Node) bool {
					switch n.(type) {
					case *BashPPInterfaceType, *BashPPTypeAssertExpr:
						t.Fatalf("%v produced interface AST for %q", lang, src)
					}
					return true
				})
			}
		}
	}
}
