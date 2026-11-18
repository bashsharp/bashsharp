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

func TestBashPPPredeclaredBuiltinAST(t *testing.T) {
	const src = "func main() {\n\ts := make([]int, 0, 4)\n\tn := len(s)\n\ts = append(s, 1)\n\tprintln(n)\n}\n"
	parse := func(rd io.Reader) *File {
		f, err := NewParser(Variant(LangBashPP)).Parse(rd, "builtins.bpp")
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	buffered := parse(strings.NewReader(src))
	oneByte := parse(iotest.OneByteReader(strings.NewReader(src)))
	if !reflect.DeepEqual(buffered, oneByte) {
		t.Fatal("buffered and one-byte builtin trees differ")
	}
	body := buffered.Stmts[0].Cmd.(*BashPPFuncDecl).Body.Stmts
	makeCall := body[0].Cmd.(*BashPPShortDecl).Call
	if makeCall == nil || makeCall.ArgType == nil {
		t.Fatalf("make call lost its type argument: %#v", makeCall)
	}
	for _, call := range []*BashPPCall{makeCall, body[1].Cmd.(*BashPPShortDecl).Call, body[3].Cmd.(*BashPPCall)} {
		if !call.Lparen.IsValid() || !call.Rparen.IsValid() || call.Pos().Offset() >= call.End().Offset() {
			t.Fatalf("call has invalid positions: %#v", call)
		}
	}
	seenType := false
	Walk(buffered, func(node Node) bool {
		if _, ok := node.(*BashPPCollectionType); ok {
			seenType = true
		}
		return true
	})
	if !seenType {
		t.Fatal("Walk missed make's type argument")
	}
	var printed bytes.Buffer
	if err := NewPrinter().Print(&printed, buffered); err != nil {
		t.Fatal(err)
	}
	if reparsed := parse(strings.NewReader(printed.String())); !reflect.DeepEqual(buffered, reparsed) {
		t.Fatalf("printed builtin tree changed\n%s", printed.String())
	}
}

func TestBashPPPredeclaredBuiltinFallback(t *testing.T) {
	const src = "len() { :; }\ncap() { :; }\nappend() { :; }\ncopy() { :; }\nmin() { :; }\nmax() { :; }\nnew() { :; }\nmake() { :; }\n"
	for _, lang := range []LangVariant{LangBash, LangPOSIX} {
		f, err := NewParser(Variant(lang)).Parse(strings.NewReader(src), "")
		if err != nil {
			t.Fatal(err)
		}
		Walk(f, func(node Node) bool {
			if _, ok := node.(*BashPPCall); ok {
				t.Fatalf("%v claimed a Bash++ builtin call", lang)
			}
			return true
		})
	}
}

func TestBashPPPredeclaredBuiltinResidualAST(t *testing.T) {
	const src = "func main() {\n\tch := make(chan int, 2)\n\tp := new([3]int)\n\tbuf := make([]byte, 0, 4)\n\ta := len(ch)\n\tb := cap(p)\n\tbuf = append(buf, \"x\"...)\n\tn := copy(buf, \"y\")\n\tm := max(9007199254740992, 9007199254740993)\n}\n"
	parse := func(rd io.Reader) *File {
		f, err := NewParser(Variant(LangBashPP)).Parse(rd, "builtin-residual.bpp")
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	buffered := parse(strings.NewReader(src))
	streamed := parse(iotest.OneByteReader(strings.NewReader(src)))
	if !reflect.DeepEqual(buffered, streamed) {
		t.Fatal("buffered and streamed residual builtin trees differ")
	}
	body := buffered.Stmts[0].Cmd.(*BashPPFuncDecl).Body.Stmts
	appendAssign, ok := body[5].Cmd.(*BashPPAssign)
	if !ok {
		t.Fatalf("append assignment node = %T", body[5].Cmd)
	}
	appendCall := appendAssign.Call
	if appendCall == nil || appendCall.Fun[0].Value != "append" || !appendCall.Ellipsis.IsValid() {
		t.Fatalf("append string spread = %#v", appendCall)
	}
	for _, index := range []int{3, 4, 6, 7} {
		decl := body[index].Cmd.(*BashPPShortDecl)
		if decl.Call == nil || !decl.Call.Lparen.IsValid() || !decl.Call.Rparen.IsValid() {
			t.Fatalf("builtin statement %d lost call positions: %#v", index, decl)
		}
	}
	var printed bytes.Buffer
	if err := NewPrinter().Print(&printed, buffered); err != nil || printed.String() != src {
		t.Fatalf("print = %q, %v", printed.String(), err)
	}
}
