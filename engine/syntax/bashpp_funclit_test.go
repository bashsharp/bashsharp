// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package syntax

import (
	"io"
	"strings"
	"testing"
)

// The Sprint 114 P3-B parser surface: function literals at each site that may
// hold one, and variadic parameter lists.
//
// The shapes are pinned by ROUND TRIP rather than by node inspection alone.
// A literal is the first Bash++ construct whose body, signature and invocation
// are three separately parsed regions, so a node that is structurally right
// while its positions are wrong prints differently from its source — and the
// printer is the only reader that can tell.

// bashppLitOneByteReader hands the parser one byte at a time, which is what
// makes the streaming property testable: a recognizer that peeked past the
// buffered chunk would decide differently here.
type bashppLitOneByteReader struct{ io.Reader }

func (r bashppLitOneByteReader) Read(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1]
	}
	return r.Reader.Read(p)
}

// bashppFuncLitSources is every literal and variadic spelling P3-B claims,
// written the way the printer emits them so each doubles as a round-trip case.
var bashppFuncLitSources = []string{
	// A literal bound to a name, then called by that name.
	"greet := func(who string) {\n\techo \"hi $who\"\n}\n",
	"greet := func() {\n\techo hi\n}\n",
	"add := func(a, b int) int {\n\treturn $((a + b))\n}\n",
	// Immediate invocation, at a command position and into a binding.
	"func(n int) {\n\techo $n\n}(1)\n",
	"n := func() int {\n\treturn 1\n}()\n",
	"_ := func() {\n\techo hi\n}()\n",
	// A deferred closure.
	"func f() {\n\tdefer func() {\n\t\techo done\n\t}()\n}\n",
	"func f() {\n\tdefer func(seen int) {\n\t\techo $seen\n\t}($v)\n}\n",
	// A closure escaping the function that built it.
	"func mk(base int) func {\n\treturn func(extra int) int {\n\t\treturn $((base + extra))\n\t}\n}\n",
	// Variadic declarations, and the spread that feeds them.
	"func sum(nums ...int) int {\n\treturn 0\n}\n",
	"func tag(p string, rest ...int) {\n\techo $p\n}\n",
	"func any(...int) {\n\techo hi\n}\n",
	"sum(xs...)\n",
	"sum(1, 2, 3)\n",
	"total := sum(xs...)\n",
	// A function-valued parameter, which is what a callback is spelled as.
	"func apply(cb func, n int) {\n\tcb($n)\n}\n",
}

func TestBashPPFuncLitRoundTrip(t *testing.T) {
	t.Parallel()

	for _, src := range bashppFuncLitSources {
		t.Run(src, func(t *testing.T) {
			t.Parallel()
			f, err := NewParser(Variant(LangBashPP)).Parse(strings.NewReader(src), "")
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			var out strings.Builder
			if err := NewPrinter().Print(&out, f); err != nil {
				t.Fatalf("print: %v", err)
			}
			if out.String() != src {
				t.Fatalf("print round trip produced %q, want %q", out.String(), src)
			}
			// Reparsing the printed bytes must reach the same classification;
			// a literal that only survives one pass is not a stored decision.
			f2, err := NewParser(Variant(LangBashPP)).Parse(strings.NewReader(out.String()), "")
			if err != nil {
				t.Fatalf("reparse: %v", err)
			}
			var out2 strings.Builder
			if err := NewPrinter().Print(&out2, f2); err != nil {
				t.Fatalf("reprint: %v", err)
			}
			if out2.String() != src {
				t.Fatalf("second round trip produced %q, want %q", out2.String(), src)
			}
		})
	}
}

// TestBashPPFuncLitOneByte proves the literal sites decide inside the buffered
// chunk: the same sources, fed one byte per Read, parse identically.
func TestBashPPFuncLitOneByte(t *testing.T) {
	t.Parallel()

	for _, src := range bashppFuncLitSources {
		t.Run(src, func(t *testing.T) {
			t.Parallel()
			f, err := NewParser(Variant(LangBashPP)).
				Parse(bashppLitOneByteReader{strings.NewReader(src)}, "lit.bpp")
			if err != nil {
				t.Fatalf("one-byte parse: %v", err)
			}
			var out strings.Builder
			if err := NewPrinter().Print(&out, f); err != nil {
				t.Fatalf("print: %v", err)
			}
			if out.String() != src {
				t.Fatalf("one-byte parse produced %q, want %q", out.String(), src)
			}
		})
	}
}

// TestBashPPFuncLitNodes pins WHICH node each site produces, because the
// printer alone cannot distinguish a literal bound to a name from a literal
// invoked into one: both print the bytes they were given.
func TestBashPPFuncLitNodes(t *testing.T) {
	t.Parallel()

	t.Run("bound to a name", func(t *testing.T) {
		cmd := bashppParseOne(t, "greet := func(who string) {\n\techo hi\n}\n")
		decl, ok := cmd.(*BashPPShortDecl)
		if !ok {
			t.Fatalf("got %T, want *BashPPShortDecl", cmd)
		}
		if decl.FuncLit == nil || decl.Call != nil || decl.Class != ClassR {
			t.Fatalf("got %#v, want Class R with FuncLit and no Call", decl)
		}
		if len(decl.FuncLit.Params) != 1 || decl.FuncLit.Params[0].FieldType.Value != "string" {
			t.Fatalf("params = %#v", decl.FuncLit.Params)
		}
	})

	t.Run("invoked into a binding", func(t *testing.T) {
		cmd := bashppParseOne(t, "n := func() int {\n\treturn 1\n}()\n")
		decl, ok := cmd.(*BashPPShortDecl)
		if !ok {
			t.Fatalf("got %T, want *BashPPShortDecl", cmd)
		}
		// What is bound is the RESULT, so the call is the node and the literal
		// hangs off it rather than off the declaration.
		if decl.FuncLit != nil || decl.Call == nil || decl.Call.FuncLit == nil {
			t.Fatalf("got %#v, want the call form", decl)
		}
	})

	t.Run("immediately invoked", func(t *testing.T) {
		cmd := bashppParseOne(t, "func(n int) {\n\techo $n\n}(1)\n")
		call, ok := cmd.(*BashPPCall)
		if !ok {
			t.Fatalf("got %T, want *BashPPCall", cmd)
		}
		if call.FuncLit == nil || len(call.Fun) != 0 || len(call.Args) != 1 {
			t.Fatalf("got %#v, want a literal callee with one argument", call)
		}
	})

	t.Run("deferred", func(t *testing.T) {
		cmd := bashppParseOne(t, "func f() {\n\tdefer func() {\n\t\techo done\n\t}()\n}\n")
		fd, ok := cmd.(*BashPPFuncDecl)
		if !ok {
			t.Fatalf("got %T, want *BashPPFuncDecl", cmd)
		}
		def, ok := fd.Body.Stmts[0].Cmd.(*BashPPDefer)
		if !ok {
			t.Fatalf("body stmt is %T, want *BashPPDefer", fd.Body.Stmts[0].Cmd)
		}
		if def.Call == nil || def.Call.FuncLit == nil {
			t.Fatalf("got %#v, want a deferred literal", def)
		}
	})

	t.Run("returned", func(t *testing.T) {
		cmd := bashppParseOne(t, "func mk(base int) func {\n\treturn func(extra int) int {\n\t\treturn $((base + extra))\n\t}\n}\n")
		fd, ok := cmd.(*BashPPFuncDecl)
		if !ok {
			t.Fatalf("got %T, want *BashPPFuncDecl", cmd)
		}
		if len(fd.Results) != 1 || fd.Results[0].FieldType.Value != "func" {
			t.Fatalf("results = %#v, want the bare func type", fd.Results)
		}
		ret, ok := fd.Body.Stmts[0].Cmd.(*BashPPReturn)
		if !ok {
			t.Fatalf("body stmt is %T, want *BashPPReturn", fd.Body.Stmts[0].Cmd)
		}
		if ret.FuncLit == nil || len(ret.Results) != 0 {
			t.Fatalf("got %#v, want a returned literal", ret)
		}
	})

	t.Run("variadic parameter", func(t *testing.T) {
		cmd := bashppParseOne(t, "func tag(p string, rest ...int) {\n\techo $p\n}\n")
		fd := cmd.(*BashPPFuncDecl)
		if len(fd.Params) != 2 {
			t.Fatalf("params = %#v, want two groups", fd.Params)
		}
		if fd.Params[0].Variadic() {
			t.Fatalf("fixed parameter reported as variadic: %#v", fd.Params[0])
		}
		last := fd.Params[1]
		if !last.Variadic() || last.FieldType.Value != "int" || len(last.Names) != 1 {
			t.Fatalf("variadic group = %#v, want one name of element type int", last)
		}
		// The element type keeps its own position, past the dots, so a
		// diagnostic points at the type the script wrote.
		if last.FieldType.Pos().Offset() != last.Ellipsis.Offset()+3 {
			t.Fatalf("element type at %v, dots at %v", last.FieldType.Pos(), last.Ellipsis)
		}
	})

	t.Run("spread argument", func(t *testing.T) {
		call := bashppParseOne(t, "sum(xs...)\n").(*BashPPCall)
		if !call.Ellipsis.IsValid() {
			t.Fatalf("got %#v, want a spread call", call)
		}
		if len(call.Args) != 1 || call.Args[0].Lit() != "xs" {
			t.Fatalf("args = %#v, want the bare name without its dots", call.Args)
		}
	})
}

// bashppParseOne parses src and returns its single command.
func bashppParseOne(t *testing.T, src string) Command {
	t.Helper()
	f, err := NewParser(Variant(LangBashPP)).Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	if len(f.Stmts) != 1 {
		t.Fatalf("parse %q: got %d statements, want one", src, len(f.Stmts))
	}
	return f.Stmts[0].Cmd
}

// TestBashPPFuncLitDiagnostics covers the shapes that are claimed and then
// refused. Every one of them is Class R — bash rejects it too — which is what
// licenses a diagnostic instead of a silent fallback.
func TestBashPPFuncLitDiagnostics(t *testing.T) {
	t.Parallel()

	tests := []struct{ name, src, want string }{
		{"variadic not final", "func f(a ...int, b int) {\n\techo hi\n}\n", "final parameter"},
		{"variadic shared with a name", "func f(a, b ...int) {\n\techo hi\n}\n", "final parameter"},
		{"two variadic groups", "func f(a ...int, b ...int) {\n\techo hi\n}\n", "final parameter"},
		{"variadic result", "func f() (...int) {\n\techo hi\n}\n", "final parameter"},
		{"spread not final", "f(xs..., 1)\n", "must be followed by"},
		{"literal not called", "func(n int) {\n\techo $n\n}\n", "must be called"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewParser(Variant(LangBashPP)).Parse(strings.NewReader(tc.src), "")
			if err == nil {
				t.Fatalf("parsed %q, want an error mentioning %q", tc.src, tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// TestBashPPFuncLitClassicIsolation is the compatibility half.
//
// The claimed literal shapes are all Class R, so the classic grammars must
// reject them — nothing legal is being taken away. The one shape that is NOT
// claimed, `func() { … }`, is the bash definition of a function named `func`,
// and it must keep parsing to exactly the same tree under both dialects.
func TestBashPPFuncLitClassicIsolation(t *testing.T) {
	t.Parallel()

	for _, lang := range []LangVariant{LangBash, LangPOSIX} {
		for _, src := range []string{
			"greet := func(who string) {\n\techo hi\n}\n",
			"func(n int) {\n\techo $n\n}(1)\n",
			"func sum(nums ...int) int {\n\treturn 0\n}\n",
			"sum(xs...)\n",
		} {
			if _, err := NewParser(Variant(lang)).Parse(strings.NewReader(src), ""); err == nil {
				t.Errorf("classic %v accepted %q, want a syntax error", lang, src)
			}
		}
	}

	const shellFunc = "func() { echo hi; }\n"
	for _, lang := range []LangVariant{LangBash, LangPOSIX} {
		f, err := NewParser(Variant(lang)).Parse(strings.NewReader(shellFunc), "")
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := f.Stmts[0].Cmd.(*FuncDecl); !ok {
			t.Fatalf("classic function: %T", f.Stmts[0].Cmd)
		}
	}
	bashppCheckIdentical(t, shellFunc)

	// POSIX MODE is a runtime gate, not a parse-time one: the tree survives so
	// the interpreter can refuse it with a diagnostic of its own.
	if _, err := NewParser(Variant(LangBashPP), PosixMode(true)).
		Parse(strings.NewReader(bashppFuncLitSources[0]), "lit.sh"); err != nil {
		t.Fatalf("POSIX mode should preserve the AST for runtime gating: %v", err)
	}
}

func TestSprint119ClassicFuncNameInBashFixtures(t *testing.T) {
	t.Parallel()

	// These are reduced forms of the Bash 5.3 trap/varenv fixtures that failed
	// when Bash++ treated the classic function name `func` as a reserved word.
	for _, src := range []string{
		"func() { trap 'echo trapped' USR1; }; kill -USR1 $$\n",
		"func() { kill -USR1 $$; }; trap func USR1; func\n",
		"func() { var=20 return; }; var=10; func; echo $var\n",
		"func() { printf 'arg=%s\\n' \"$1\"; }; func value\n",
	} {
		t.Run(src, func(t *testing.T) {
			bashFile, bashErr := NewParser(Variant(LangBash)).Parse(strings.NewReader(src), "")
			ppFile, ppErr := NewParser(Variant(LangBashPP)).Parse(strings.NewReader(src), "")
			if (bashErr == nil) != (ppErr == nil) {
				t.Fatalf("bash err=%v, bash++ err=%v", bashErr, ppErr)
			}
			if bashErr != nil {
				return
			}
			if diff := bashppTreeDiff(bashFile, ppFile); diff != "" {
				t.Fatalf("Bash++ changed classic fixture syntax: %s", diff)
			}
		})
	}
}

func TestSprint119ClassicFuncNameForms(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		src   string
		posix bool
	}{
		{"adjacent", "func() { echo hi; }; func\n", true},
		{"spaced", "func () { echo hi; }; func\n", true},
		{"spaced-empty-list", "func ( ) { echo hi; }; func\n", true},
		{"tabbed", "func\t() { echo hi; }; func\n", true},
		{"long-form", "function func { echo hi; }; func\n", false},
		{"long-form-parentheses", "function func () { echo hi; }; func\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// This compares AST, positions and printed bytes for both readers
			// and Bash's POSIX mode, rather than merely accepting the source.
			bashppCheckIdentical(t, tc.src)
			for _, mode := range bashppReadModes {
				f, err := bashppParseAs(LangPOSIX, tc.src, false, mode.wrap)
				if !tc.posix {
					if err == nil {
						t.Fatalf("pure POSIX grammar accepted Bash function spelling %q", tc.src)
					}
					continue
				}
				if err != nil {
					t.Fatalf("pure POSIX grammar rejected classic function: %v", err)
				}
				if _, ok := f.Stmts[0].Cmd.(*FuncDecl); !ok {
					t.Fatalf("pure POSIX function became %T", f.Stmts[0].Cmd)
				}
			}
		})
	}
}

func TestSprint119ClassicFuncCallForms(t *testing.T) {
	t.Parallel()
	parse := func(lang LangVariant, posix bool, entry string, src io.Reader) (*File, error) {
		p := NewParser(Variant(lang), KeepComments(true), PosixMode(posix))
		if entry == "Parse" {
			return p.Parse(src, "")
		}
		f := &File{}
		if entry == "Stmts" {
			for stmt, err := range p.StmtsSeq(src) {
				if err != nil {
					return nil, err
				}
				f.Stmts = append(f.Stmts, stmt)
			}
		} else {
			for stmts, err := range p.InteractiveSeq(src) {
				if err != nil {
					return nil, err
				}
				f.Stmts = append(f.Stmts, stmts...)
			}
		}
		return f, nil
	}
	// Argument-bearing and assignment-prefixed calls occur in the unchanged
	// comsub/varenv/func fixtures. They remain shell syntax independently of
	// whether a function named func has already been defined.
	for _, src := range []string{
		"func nope\n",
		"func a b c\n",
		"func IFS= read\n",
		"func name=value\n",
		"A=BVAR func\n",
		"var=40 func\n",
		"var=two func\n",
		"var=30 func\n",
		"A=value func a b c\n",
		"x=${ func a b c; }\n",
		"func() { echo \"$var:$*\"; }; var=30 func a b c\n",
	} {
		t.Run(src, func(t *testing.T) {
			for _, entry := range []string{"Parse", "Stmts", "Interactive"} {
				for _, posix := range []bool{false, true} {
					for _, mode := range bashppReadModes {
						bashFile, bashErr := parse(LangBash, posix, entry, mode.wrap(src))
						ppFile, ppErr := parse(LangBashPP, posix, entry, mode.wrap(src))
						if bashErr != nil || ppErr != nil {
							t.Fatalf("%s/%s posix=%v: bash err=%v, bash++ err=%v", entry, mode.name, posix, bashErr, ppErr)
						}
						if len(bashFile.Stmts) == 0 || len(ppFile.Stmts) == 0 {
							t.Fatalf("%s/%s lost the shell call", entry, mode.name)
						}
						if diff := bashppTreeDiff(bashFile, ppFile); diff != "" {
							t.Fatalf("%s/%s posix=%v changed shell call syntax: %s", entry, mode.name, posix, diff)
						}
					}
				}
			}
		})
	}
}

func TestSprint119FuncRoutingTypedControls(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		src  string
		kind string
	}{
		{"declaration", "func f(n int) int { return n }\n", "declaration"},
		{"method", "func (r Count) M(n int) int { return n }\n", "method"},
		{"pointer-method", "func (r *Count) M() { return; }\n", "method"},
		{"bound-literal", "greet := func(who string) { echo hi; }\n", "bound-literal"},
		{"called-literal", "func(n int) { echo $n; }(1)\n", "called-literal"},
		{"bound-empty-literal", "n := func() int { return 1 }()\n", "bound-empty-literal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, mode := range bashppReadModes {
				for _, posixMode := range []bool{false, true} {
					// Explicit library grammar selection remains separate from
					// Bashy's startup resolver and the interpreter's POSIX gate.
					f, err := bashppParseAs(LangBashPP, tc.src, posixMode, mode.wrap)
					if err != nil {
						t.Fatalf("typed syntax rejected (%s posix=%v): %v", mode.name, posixMode, err)
					}
					cmd := f.Stmts[0].Cmd
					switch tc.kind {
					case "declaration", "method":
						fd, ok := cmd.(*BashPPFuncDecl)
						if !ok || (fd.Receiver != nil) != (tc.kind == "method") {
							t.Fatalf("typed declaration routing changed: %T %#v", cmd, cmd)
						}
					case "bound-literal", "bound-empty-literal":
						decl, ok := cmd.(*BashPPShortDecl)
						if !ok || (tc.kind == "bound-literal" && decl.FuncLit == nil) ||
							(tc.kind == "bound-empty-literal" && (decl.Call == nil || decl.Call.FuncLit == nil)) {
							t.Fatalf("bound literal routing changed: %T %#v", cmd, cmd)
						}
					case "called-literal":
						call, ok := cmd.(*BashPPCall)
						if !ok || call.FuncLit == nil {
							t.Fatalf("literal callee routing changed: %T %#v", cmd, cmd)
						}
					}
				}
				for _, lang := range []LangVariant{LangBash, LangPOSIX} {
					if _, err := bashppParseAs(lang, tc.src, false, mode.wrap); err == nil {
						t.Fatalf("classic %v accepted typed syntax %q", lang, tc.src)
					}
				}
			}
		})
	}
	for _, src := range []string{
		"func f(",
		"func f(a ...int, b int) { return; }",
		"func () M() { return; }",
		"func (r) M() { return; }",
		"func(n int) { echo $n; }", // A command-position literal must be invoked.
	} {
		for _, posixMode := range []bool{false, true} {
			for _, mode := range bashppReadModes {
				_, err := bashppParseAs(LangBashPP, src, posixMode, mode.wrap)
				pe, ok := err.(ParseError)
				if !ok || pe.Pos.Line() == 0 || pe.Pos.Col() == 0 {
					t.Fatalf("malformed typed prefix lost its positioned diagnostic: %q: %v", src, err)
				}
			}
		}
	}
}

// TestBashPPFuncLitStartSites pins the decision table entries the literal sites
// rest on, without a parser in the way.
func TestBashPPFuncLitStartSites(t *testing.T) {
	t.Parallel()

	tests := []struct {
		src  string
		want StartSite
	}{
		{"func(n int) { echo $n }(1)", StartFuncLit},
		// Not claimed: only a non-empty parameter list commits at a command
		// position, because `func()` is the bash function definition prefix.
		{"func() int { return 1 }()", StartNone},
		{"func (n int) { echo $n }(1)", StartFuncLit},
		// Not claimed: the bash function definition of a function named func.
		{"func() { echo hi; }", StartNone},
		{"func () { echo hi; }", StartNone},
		// A named declaration still wins, and `defer` still owns its own site.
		{"func f(a int) int { return a }", StartFunc},
		{"defer func() { echo done }()", StartDefer},
		{"defer func(n int) { echo $n }(1)", StartDefer},
		{"defer cleanup", StartNone},
		// The literal in a := right-hand side is the short-declaration site,
		// which already commits on the parenthesis after the name.
		{"greet := func(who string) { echo hi }", StartShortDecl},
	}
	for _, tc := range tests {
		t.Run(tc.src, func(t *testing.T) {
			t.Parallel()
			m := RecognizeStartSite(tc.src)
			if m.Site != tc.want {
				t.Fatalf("site = %v, want %v", m.Site, tc.want)
			}
			if tc.want == StartNone {
				return
			}
			if m.Class != ClassR {
				t.Fatalf("class = %v, want R: every literal site is a shape bash rejects", m.Class)
			}
			if !m.Bounded {
				t.Fatalf("site %v was not decided within the lookahead budget", m.Site)
			}
			if len(tc.src) > maxLookahead {
				t.Fatalf("case is longer than the lookahead budget")
			}
		})
	}
}

// TestBashPPFuncLitWalk proves the new node is reachable by the visitor. Walk
// panics on a node type it does not know, so an unwired node is not a missing
// feature but a crash in any consumer that walks a tree containing one.
func TestBashPPFuncLitWalk(t *testing.T) {
	t.Parallel()

	f, err := NewParser(Variant(LangBashPP)).Parse(strings.NewReader(
		"greet := func(who string, rest ...int) {\n\techo hi\n}\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	var lits, fields int
	Walk(f, func(n Node) bool {
		switch n := n.(type) {
		case *BashPPFuncLit:
			lits++
		case *BashPPField:
			fields++
			_ = n.Variadic()
		}
		return true
	})
	if lits != 1 || fields != 2 {
		t.Fatalf("walk saw %d literals and %d fields, want 1 and 2", lits, fields)
	}
}
