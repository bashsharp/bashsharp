package syntax

import (
	"bytes"
	"io"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
)

const bashppConstGroupSource = "const (\n\t# zero\n\tA = iota\n\tB\n\tC int = iota + 2\n\t# end\n)\n"

func TestBashPPConstGroupStreamingPrintAndWalk(t *testing.T) {
	parse := func(rd io.Reader) *File {
		f, err := NewParser(Variant(LangBashPP), KeepComments(true)).Parse(rd, "const.bpp")
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	buffered := parse(strings.NewReader(bashppConstGroupSource))
	streamed := parse(iotest.OneByteReader(strings.NewReader(bashppConstGroupSource)))
	if !reflect.DeepEqual(buffered, streamed) {
		t.Fatal("buffered and one-byte const trees differ")
	}
	group, ok := buffered.Stmts[0].Cmd.(*BashPPConstGroup)
	if !ok || len(group.Specs) != 3 || group.Specs[1].InitExpr != nil || group.Specs[2].Iota != 2 {
		t.Fatalf("const group = %#v", buffered.Stmts[0].Cmd)
	}
	if len(group.Specs[0].Comments) != 1 || len(group.Last) != 1 {
		t.Fatalf("const comments = first %d, last %d", len(group.Specs[0].Comments), len(group.Last))
	}
	seenGroup, seenSpecs := false, 0
	Walk(buffered, func(n Node) bool {
		switch n.(type) {
		case *BashPPConstGroup:
			seenGroup = true
		case *BashPPConstSpec:
			seenSpecs++
		}
		return true
	})
	if !seenGroup || seenSpecs != 3 {
		t.Fatalf("walk group/specs = %v/%d", seenGroup, seenSpecs)
	}
	var out bytes.Buffer
	if err := NewPrinter().Print(&out, buffered); err != nil || out.String() != bashppConstGroupSource {
		t.Fatalf("print = %q, %v", out.String(), err)
	}
}

func TestBashPPConstGroupDialectIsolationAndDiagnostics(t *testing.T) {
	for _, lang := range []LangVariant{LangBash, LangPOSIX} {
		f, _ := NewParser(Variant(lang)).Parse(strings.NewReader(bashppConstGroupSource), "const.sh")
		if f != nil {
			Walk(f, func(n Node) bool {
				if _, ok := n.(*BashPPConstGroup); ok {
					t.Fatalf("%v produced const group", lang)
				}
				return true
			})
		}
	}
	for _, src := range []string{"const ( A )\n", "const ( A = )\n", "const ( A = iota B = iota )\n", "const ( A = iota\n"} {
		if _, err := NewParser(Variant(LangBashPP)).Parse(strings.NewReader(src), "bad.bpp"); err == nil || !strings.Contains(err.Error(), "bash++ const") {
			t.Fatalf("parse %q error = %v", src, err)
		}
	}
}
