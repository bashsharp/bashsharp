// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package syntax

import (
	"io"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
)

const bashPPSelectorKeyFixture = `type Leaf struct { Depth int }
type Habitat struct { Leaf }
type Gopher struct { Name string; Habitat }
func main() {
	g := Gopher{Name: "x", Depth: 3}
}
`

func TestBashPPStructSelectorKeyStreamingWalkPrint(t *testing.T) {
	parse := func(rd io.Reader) *File {
		file, err := NewParser(Variant(LangBashPP)).Parse(rd, "selector-key.bpp")
		if err != nil {
			t.Fatal(err)
		}
		return file
	}
	buffered := parse(strings.NewReader(bashPPSelectorKeyFixture))
	streamed := parse(iotest.OneByteReader(strings.NewReader(bashPPSelectorKeyFixture)))
	if !reflect.DeepEqual(buffered, streamed) {
		t.Fatal("buffered and one-byte selector-key trees differ")
	}
	body := buffered.Stmts[3].Cmd.(*BashPPFuncDecl).Body
	lit := body.Stmts[0].Cmd.(*BashPPShortDecl).Expr.(*BashPPCompositeLit)
	key, ok := lit.Elems[1].Key.(*BashPPIdent)
	if !ok || key.Name.Value != "Depth" || !key.Pos().IsValid() || !key.End().IsValid() {
		t.Fatalf("promoted key = %#v", lit.Elems[1].Key)
	}
	seenKey := false
	Walk(buffered, func(node Node) bool {
		if id, ok := node.(*BashPPIdent); ok && id.Name.Value == "Depth" {
			seenKey = true
		}
		return true
	})
	if !seenKey {
		t.Fatal("Walk missed promoted field key")
	}
	var printed strings.Builder
	if err := NewPrinter().Print(&printed, buffered); err != nil {
		t.Fatal(err)
	}
	if printed.String() != bashPPSelectorKeyFixture {
		t.Fatalf("print = %q", printed.String())
	}
}

func TestBashPPStructSelectorKeyDialectIsolation(t *testing.T) {
	const src = "g := Gopher{Name: \"x\", Burrow: \"deep\"}\nGopher{Burrow: \"deep\"}\n"
	for _, lang := range []LangVariant{LangBash, LangPOSIX} {
		for _, wrap := range []func(io.Reader) io.Reader{
			func(r io.Reader) io.Reader { return r },
			func(r io.Reader) io.Reader { return iotest.OneByteReader(r) },
		} {
			file, err := NewParser(Variant(lang)).Parse(wrap(strings.NewReader(src)), "")
			if err != nil {
				t.Fatal(err)
			}
			Walk(file, func(node Node) bool {
				if _, ok := node.(*BashPPCompositeLit); ok {
					t.Fatalf("%v produced a Bash++ composite", lang)
				}
				return true
			})
		}
	}
}
