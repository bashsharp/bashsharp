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

func TestBashPPKeywordAssignmentShellParity(t *testing.T) {
	for _, src := range []string{
		`while IFS= read -r line; do echo "$line"; done </dev/null`,
		`drain() { while IFS= read -r line; do echo "$line"; done; }`,
		`function drain { while IFS= read -r line; do echo "$line"; done; }`,
		`until CHECK= ready; do sleep 1; done`,
		`if FLAG= ready; then echo yes; fi`,
		`time MODE= probe`,
		`probe EMPTY= value`,
		`probe --output= value`,
		`command var IFS= read`,
		`func IFS= read`,
		`func name=value`,
	} {
		t.Run(src, func(t *testing.T) {
			want, err := NewParser(Variant(LangBash)).Parse(strings.NewReader(src), "")
			if err != nil {
				t.Fatal(err)
			}
			for _, reader := range []io.Reader{strings.NewReader(src), iotest.OneByteReader(strings.NewReader(src))} {
				got, err := NewParser(Variant(LangBashPP)).Parse(reader, "")
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatal("Bash++ changed the shell AST")
				}
			}
		})
	}
}

func TestBashPPKeywordAssignmentTypedStarts(t *testing.T) {
	for _, op := range []string{"+=", "-=", "*=", "/=", "%=", "&=", "|=", "^=", "<<=", ">>=", "&^="} {
		for _, name := range []string{"x", "while"} {
			src := name + " " + op + " 1"
			for _, reader := range []io.Reader{strings.NewReader(src), iotest.OneByteReader(strings.NewReader(src))} {
				f, err := NewParser(Variant(LangBashPP)).Parse(reader, "")
				if err != nil {
					t.Fatalf("%s: %v", src, err)
				}
				update, ok := f.Stmts[0].Cmd.(*BashPPUpdate)
				if !ok || update.Op.Value != op {
					t.Fatalf("%s: want typed update, got %#v", src, f.Stmts[0].Cmd)
				}
			}
		}
	}
	for _, src := range []string{"var IFS= read", "const IFS= read", "import IFS= read", "package IFS= read", "goto IFS= read", "x :="} {
		for _, reader := range []io.Reader{strings.NewReader(src), iotest.OneByteReader(strings.NewReader(src))} {
			if _, err := NewParser(Variant(LangBashPP)).Parse(reader, ""); err == nil {
				t.Fatalf("%s: reserved form fell back to a shell command", src)
			}
		}
	}
}

func TestBashPPKeywordAssignmentInGoFunction(t *testing.T) {
	src := `func drain() { while IFS= read -r line; do echo "$line"; done </dev/null; }`
	for _, reader := range []io.Reader{strings.NewReader(src), iotest.OneByteReader(strings.NewReader(src))} {
		f, err := NewParser(Variant(LangBashPP)).Parse(reader, "")
		if err != nil {
			t.Fatal(err)
		}
		fn, ok := f.Stmts[0].Cmd.(*BashPPFuncDecl)
		if !ok {
			t.Fatalf("want Go function, got %T", f.Stmts[0].Cmd)
		}
		loop, ok := fn.Body.Stmts[0].Cmd.(*WhileClause)
		if !ok {
			t.Fatalf("want shell loop, got %T", fn.Body.Stmts[0].Cmd)
		}
		condition, ok := loop.Cond[0].Cmd.(*CallExpr)
		if !ok || len(condition.Assigns) != 1 || condition.Assigns[0].Name.Value != "IFS" || condition.Args[0].Lit() != "read" {
			t.Fatalf("shell condition changed: %#v", loop.Cond[0].Cmd)
		}
	}
}
