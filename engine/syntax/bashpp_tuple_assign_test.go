// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package syntax

import (
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

func TestBashPPTupleAssignStreamingRoundTrip(t *testing.T) {
	for _, src := range []string{
		"func f() {\n\tx, y = y, x\n}\n",
		"func f() {\n\tx = x + 1\n}\n",
		"func pair() (int, int) {\n\treturn 1, 2\n}\nfunc f() {\n\tx, y = pair()\n}\n",
	} {
		for _, wrap := range []func(io.Reader) io.Reader{
			func(r io.Reader) io.Reader { return r },
			func(r io.Reader) io.Reader { return iotest.OneByteReader(r) },
		} {
			file, err := NewParser(Variant(LangBashPP)).Parse(wrap(strings.NewReader(src)), "tuple.bpp")
			if err != nil {
				t.Fatal(err)
			}
			fn := file.Stmts[len(file.Stmts)-1].Cmd.(*BashPPFuncDecl)
			assign := fn.Body.Stmts[0].Cmd.(*BashPPAssign)
			if len(assign.Names) == 0 || (assign.Call == nil && (len(assign.Values) != len(assign.Names) || len(assign.ValueExprs) != len(assign.Names))) {
				t.Fatalf("tuple shape = %#v", assign)
			}
			var out strings.Builder
			if err := NewPrinter().Print(&out, file); err != nil {
				t.Fatal(err)
			}
			if out.String() != src {
				t.Fatalf("round trip = %q, want %q", out.String(), src)
			}
		}
	}
}

func TestBashPPTupleAssignRejectedRHSStreamingRoundTrip(t *testing.T) {
	const src = "func f() {\n\tx, y = x + 1, y + 2\n}\n"
	for _, wrap := range []func(io.Reader) io.Reader{
		func(r io.Reader) io.Reader { return r },
		func(r io.Reader) io.Reader { return iotest.OneByteReader(r) },
	} {
		file, err := NewParser(Variant(LangBashPP)).Parse(wrap(strings.NewReader(src)), "tuple.bpp")
		if err != nil {
			t.Fatal(err)
		}
		assign := file.Stmts[0].Cmd.(*BashPPFuncDecl).Body.Stmts[0].Cmd.(*BashPPAssign)
		if len(assign.Values) == 0 || len(assign.ValueExprs) != 0 {
			t.Fatalf("rejected tuple shape = %#v", assign)
		}
		var out strings.Builder
		if err := NewPrinter().Print(&out, file); err != nil {
			t.Fatal(err)
		}
		if out.String() != src {
			t.Fatalf("round trip = %q, want %q", out.String(), src)
		}
	}
}

func TestBashPPTupleAssignWalkOptionalFields(t *testing.T) {
	const src = "func pair() (int, int) {\n\treturn 1, 2\n}\nfunc f() {\n\tx, y = y, x\n\tx, y = pair()\n}\n"
	file, err := NewParser(Variant(LangBashPP)).Parse(strings.NewReader(src), "walk.bpp")
	if err != nil {
		t.Fatal(err)
	}
	body := file.Stmts[1].Cmd.(*BashPPFuncDecl).Body.Stmts
	for i, stmt := range body {
		assign := stmt.Cmd.(*BashPPAssign)
		seenNames, seenCall := 0, false
		Walk(assign, func(node Node) bool {
			switch node.(type) {
			case *Lit:
				seenNames++
			case *BashPPCall:
				seenCall = true
			}
			return true
		})
		if seenNames < 2 {
			t.Fatalf("assignment %d walk saw %d literals", i, seenNames)
		}
		if (i == 1) != seenCall {
			t.Fatalf("assignment %d call seen = %v", i, seenCall)
		}
	}
}

func TestBashPPTupleAssignClassicPOSIXIsolation(t *testing.T) {
	const src = "x, y = y, x\n"
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
				if _, ok := node.(*BashPPAssign); ok {
					t.Fatalf("%v claimed tuple assignment", lang)
				}
				return true
			})
		}
	}
}
