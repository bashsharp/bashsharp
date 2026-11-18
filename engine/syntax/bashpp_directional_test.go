package syntax

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

type directionalByteReader struct{ source string }

func (r *directionalByteReader) Read(p []byte) (int, error) {
	if r.source == "" {
		return 0, io.EOF
	}
	p[0] = r.source[0]
	r.source = r.source[1:]
	return 1, nil
}

func TestBashPPDirectionalChannelSignatures(t *testing.T) {
	for _, typ := range []string{"chan int", "chan<- int", "<-chan int", "chan<- []int", "<-chan map[string]int", "chan<- chan int"} {
		source := "func f(ch " + typ + ") " + typ + " {\n return ch\n}\n"
		for _, bytewise := range []bool{false, true} {
			var input io.Reader = strings.NewReader(source)
			if bytewise {
				input = &directionalByteReader{source}
			}
			file, err := NewParser(Variant(LangBashPP)).Parse(input, "direction.bpp")
			if err != nil {
				t.Fatalf("%s bytewise=%v: %v", typ, bytewise, err)
			}
			decl := file.Stmts[0].Cmd.(*BashPPFuncDecl)
			channel, ok := decl.Params[0].FieldTypeExpr.(*BashPPChanType)
			if !ok || channel.Element == nil {
				t.Fatalf("lost channel type: %#v", decl.Params[0])
			}
			if got := source[channel.Pos().Offset():channel.End().Offset()]; got != typ {
				t.Fatalf("type span %q want %q", got, typ)
			}
			if channel.Direction != "" {
				if got := source[channel.Arrow.Offset() : channel.Arrow.Offset()+2]; got != "<-" {
					t.Fatalf("arrow span %q", got)
				}
			}
			nodes := 0
			Walk(channel, func(n Node) bool {
				if n == channel.Elem {
					t.Fatal("Walk duplicated legacy element")
				}
				if _, ok := n.(*BashPPChanType); ok {
					nodes++
				}
				return true
			})
			if nodes == 0 {
				t.Fatal("Walk missed channel")
			}
			var printed bytes.Buffer
			if err := NewPrinter().Print(&printed, file); err != nil {
				t.Fatal(err)
			}
			again, err := NewParser(Variant(LangBashPP)).Parse(strings.NewReader(printed.String()), "direction.bpp")
			if err != nil {
				t.Fatalf("printed=%q: %v", printed.String(), err)
			}
			got := again.Stmts[0].Cmd.(*BashPPFuncDecl).Params[0].FieldTypeExpr
			if bashppTypeText(got) != typ {
				t.Fatalf("printed type %q want %q", bashppTypeText(got), typ)
			}
		}
	}
}

func TestBashPPDirectionalChannelShellIsolation(t *testing.T) {
	for _, variant := range []LangVariant{LangBash, LangPOSIX, LangBashPP} {
		source := "cat <-chan\necho chan<- int\n"
		file, err := NewParser(Variant(variant)).Parse(strings.NewReader(source), "shell.sh")
		if err != nil {
			t.Fatal(err)
		}
		Walk(file, func(n Node) bool {
			if _, ok := n.(*BashPPChanType); ok {
				t.Fatal("shell redirect claimed as channel")
			}
			return true
		})
		if len(file.Stmts[0].Redirs) != 1 || len(file.Stmts[1].Redirs) != 1 {
			t.Fatal("shell redirection lost")
		}
	}
}
