// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package syntax

import (
	"fmt"
	"testing"
)

func TestSprint198StartSites(t *testing.T) {
	for _, tc := range []struct {
		src string
		cmd Command
	}{
		{"x = 1", &BashPPAssign{}}, {"x += 1", &BashPPUpdate{}}, {"x++", &BashPPIncDec{}},
		{"*p = 2", &BashPPAssign{}}, {"x, y = 1, 2", &BashPPAssign{}}, {"x, y = pair()", &BashPPAssign{}},
		{"p = new(int)", &BashPPAssign{}}, {"x := 1 + 2", &BashPPShortDecl{}}, {"p := &x", &BashPPShortDecl{}},
		{"ch <- 1", &BashPPSend{}}, {"<-ch", &BashPPReceive{}},
		{"done := 1", &BashPPShortDecl{}}, {"then = 2", &BashPPAssign{}}, {"fi += 1", &BashPPUpdate{}},
		{"type foo bar", &BashPPDecl{}}, {"f[int](1)", &BashPPCall{}},
		{"goto end\nend: echo yes", &BashPPGoto{}},
	} {
		for _, posix := range []bool{false, true} {
			for _, mode := range bashppReadModes {
				t.Run(tc.src+mode.name+fmt.Sprint(posix), func(t *testing.T) {
					f, e := bashppParseAs(LangBashPP, tc.src, posix, mode.wrap)
					if e != nil {
						t.Fatal(e)
					}
					if got, want := fmt.Sprintf("%T", f.Stmts[0].Cmd), fmt.Sprintf("%T", tc.cmd); got != want {
						t.Fatalf("got %s want %s", got, want)
					}
					Walk(f, func(Node) bool { return true })
				})
			}
		}
	}
}

func TestSprint198Diagnostics(t *testing.T) {
	for _, src := range []string{"var nope", "const nope", "func nope(", "import nope", "package main", "echo ok\npackage main", "goto", "x :=", "x := map[string]int{", "f[int]", "var if = 1"} {
		for _, mode := range bashppReadModes {
			f, e := bashppParseAs(LangBashPP, src, false, mode.wrap)
			if e == nil {
				t.Errorf("%q accepted: %#v", src, f)
				continue
			}
			if _, ok := e.(ParseError); !ok {
				t.Errorf("%q error %T", src, e)
			}
		}
	}
	for _, name := range []string{"var", "const", "import", "package", "goto"} {
		for _, src := range []string{name + "() { :; }", "function " + name + " { :; }", "\"" + name + "\"() { :; }"} {
			if _, e := bashppParse(LangBashPP, src); e == nil {
				t.Errorf("%q: %v", src, e)
			}
		}
	}
}

func TestSprint198ShellControls(t *testing.T) {
	for _, src := range []string{"x=5", "x+=1", "command x = 1", "command x += 1", "command f[int]", `"f[int]"`, `"x++"`, `"x" += 1`, "command goto end", `"goto" end`, `"end:" echo yes`, "type foo", "type -a foo", "type foo >out", "go build", "defer cleanup", "return 1", "time --", "echo $((a+=1))", "if true; then echo yes; fi", "for x in a; do echo $x; done"} {
		bashppCheckIdentical(t, src)
	}
	for _, name := range []string{"var", "const", "func", "import", "package", "goto"} {
		bashppCheckIdentical(t, "command "+name+" nonsense")
		bashppCheckIdentical(t, "\""+name+"\" nonsense")
	}
}

func TestSprint198Comments(t *testing.T) {
	src := "//first\necho one //tail\necho http://x \"//path\" x//y \"$a\"//b\n"
	for _, mode := range bashppReadModes {
		f, e := bashppParseAs(LangBashPP, src, false, mode.wrap)
		if e != nil {
			t.Fatal(e)
		}
		if len(f.Stmts) != 2 {
			t.Fatalf("statements %d", len(f.Stmts))
		}
		first := f.Stmts[0].Cmd.(*CallExpr)
		if len(first.Args) != 2 {
			t.Fatalf("comment became args: %#v", first.Args)
		}
		if len(f.Stmts[1].Cmd.(*CallExpr).Args) != 5 {
			t.Fatal("literal slash word lost")
		}
		if len(f.Stmts[0].Comments) != 2 {
			t.Fatalf("comments %#v", f.Stmts[0].Comments)
		}
	}
	for _, src := range []string{"echo http://x", `echo "//path" x//y "$a"//b`, "echo / /x", "echo /*x*/"} {
		bashppCheckIdentical(t, src)
	}
}

func TestSprint198LabelScopes(t *testing.T) {
	for _, src := range []string{"goto missing", "a: echo one\na: echo two", "goto inner\n{ inner: echo no; }", "goto later\nx := 1\nlater: echo no", "goto inner\nfunc f() { inner: echo no; }", "outer: echo yes\nfunc f() { goto outer; }"} {
		if _, e := bashppParse(LangBashPP, src); e == nil {
			t.Errorf("accepted invalid labels %q", src)
		}
	}
	for _, src := range []string{"a: echo yes\ngoto a", "goto end\necho no\nend: echo yes", "a: echo yes\nfunc f() { a: echo yes; goto a; }", "{ goto end; }\nend: echo yes"} {
		if _, e := bashppParse(LangBashPP, src); e != nil {
			t.Errorf("%q: %v", src, e)
		}
	}
}

func TestSprint198SpacedOptionsRemainShell(t *testing.T) {
	for _, src := range []string{"arbitrary --", "arbitrary ++", "set --", "printf --", "suspend --", "x --", "x ++"} {
		bashppCheckIdentical(t, src)
	}
	for _, src := range []string{"x--", "x++"} {
		for _, mode := range bashppReadModes {
			f, err := bashppParseAs(LangBashPP, src, false, mode.wrap)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := f.Stmts[0].Cmd.(*BashPPIncDec); !ok {
				t.Fatalf("%q: %T", src, f.Stmts[0].Cmd)
			}
		}
	}
	for _, src := range []string{"func f() { x --; }", "func f() { x ++; }"} {
		for _, mode := range bashppReadModes {
			f, err := bashppParseAs(LangBashPP, src, false, mode.wrap)
			if err != nil {
				t.Fatal(err)
			}
			body := f.Stmts[0].Cmd.(*BashPPFuncDecl).Body
			if _, ok := body.Stmts[0].Cmd.(*BashPPIncDec); !ok {
				t.Fatalf("%q: %T", src, body.Stmts[0].Cmd)
			}
		}
	}
}
