// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package syntax

import (
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
)

type bashppByteReader struct{ io.Reader }

func (r bashppByteReader) Read(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1]
	}
	return r.Reader.Read(p)
}

func TestBashPPShortDeclSupportedShapes(t *testing.T) {
	shapes := []string{
		"x := 42", `x := "hello"`, "x, y := 1, 2",
		`m := map[string]int{"a": 1}`, "s := []int{1, 2, 3}",
		`g := Gopher{Name: "x"}`, "f := Max[int]",
		"x := f()", "x := f(1, 2)", `config, err := readConfig("config.json")`,
	}
	for _, src := range shapes {
		t.Run(src, func(t *testing.T) {
			for _, posix := range []bool{false, true} {
				for _, bytewise := range []bool{false, true} {
					var rd io.Reader = strings.NewReader(src)
					if bytewise {
						rd = bashppByteReader{rd}
					}
					f, err := NewParser(Variant(LangBashPP), PosixMode(posix)).Parse(rd, "shape.sh")
					if err != nil {
						t.Fatalf("posix=%v bytewise=%v: %v", posix, bytewise, err)
					}
					d, ok := f.Stmts[0].Cmd.(*BashPPShortDecl)
					if !ok {
						t.Fatalf("bytewise=%v: got %T", bytewise, f.Stmts[0].Cmd)
					}
					if d.Pos().Offset() != 0 || d.End().Offset() != uint(len(src)) {
						t.Fatalf("span [%d,%d), want [0,%d)", d.Pos().Offset(), d.End().Offset(), len(src))
					}
				}
			}
		})
	}
}

func TestBashPPScalarExpressionReachabilityIsGoRegionBounded(t *testing.T) {
	const src = "func main() {\n\tx := string(1)\n}\n"
	for _, bytewise := range []bool{false, true} {
		var rd io.Reader = strings.NewReader(src)
		if bytewise {
			rd = bashppByteReader{rd}
		}
		f, err := NewParser(Variant(LangBashPP)).Parse(rd, "expr.sh")
		if err != nil {
			t.Fatalf("bytewise=%v: %v", bytewise, err)
		}
		fn := f.Stmts[0].Cmd.(*BashPPFuncDecl)
		d, ok := fn.Body.Stmts[0].Cmd.(*BashPPShortDecl)
		if !ok {
			t.Fatalf("bytewise=%v: got %T", bytewise, fn.Body.Stmts[0].Cmd)
		}
		if !d.GoRegion {
			t.Fatalf("bytewise=%v: scalar expression was not marked as committed Go", bytewise)
		}
		if d.Expr == nil || d.Rhs != nil {
			t.Fatalf("scalar expression was not represented by the typed tree: %#v", d)
		}
		if got, want := d.Expr.Pos().Line(), uint(2); got != want {
			t.Fatalf("expression starts on line %d, want %d", got, want)
		}
		if _, ok := d.Expr.(*BashPPConvertExpr); !ok {
			t.Fatalf("expression = %T, want *BashPPConvertExpr", d.Expr)
		}
	}
}

func TestBashPPScalarExpressionSourceReachability(t *testing.T) {
	tests := []struct {
		expr     string
		typeName string
	}{
		{"42", "*syntax.BashPPBasicLit"},
		{`"hello"`, "*syntax.BashPPBasicLit"},
		{"base", "*syntax.BashPPIdent"},
		{"+1", "*syntax.BashPPUnaryExpr"},
		{"-1", "*syntax.BashPPUnaryExpr"},
		{"!true", "*syntax.BashPPUnaryExpr"},
		{"^1", "*syntax.BashPPUnaryExpr"},
		{"1 + 2", "*syntax.BashPPBinaryExpr"},
		{"1-2", "*syntax.BashPPBinaryExpr"},
		{"1^2", "*syntax.BashPPBinaryExpr"},
		{"1*2", "*syntax.BashPPBinaryExpr"},
		{"4/2", "*syntax.BashPPBinaryExpr"},
		{"5%2", "*syntax.BashPPBinaryExpr"},
		{"1 == 2", "*syntax.BashPPBinaryExpr"},
		{"1 != 2", "*syntax.BashPPBinaryExpr"},
		{"string(65)", "*syntax.BashPPConvertExpr"},
	}
	for _, test := range tests {
		t.Run(test.expr, func(t *testing.T) {
			src := "func main() {\n\tx := " + test.expr + "\n}\n"
			for _, bytewise := range []bool{false, true} {
				var rd io.Reader = strings.NewReader(src)
				if bytewise {
					rd = bashppByteReader{rd}
				}
				f, err := NewParser(Variant(LangBashPP)).Parse(rd, "expr.bpp")
				if err != nil {
					t.Fatalf("bytewise=%v: %v", bytewise, err)
				}
				fn := f.Stmts[0].Cmd.(*BashPPFuncDecl)
				decl, ok := fn.Body.Stmts[0].Cmd.(*BashPPShortDecl)
				if !ok || decl.Expr == nil {
					t.Fatalf("bytewise=%v: declaration = %#v", bytewise, fn.Body.Stmts[0].Cmd)
				}
				if got := fmt.Sprintf("%T", decl.Expr); got != test.typeName {
					t.Fatalf("bytewise=%v: expression = %s, want %s", bytewise, got, test.typeName)
				}
			}
		})
	}
}

// bashppShellOperatorExprs are the Go binary operators spelled with shell
// metacharacters. Each is claimed only inside a committed Go region, and only
// through the carrier in bashpp_scalar.go, since the ordinary shell parse would
// otherwise have turned it into a pipeline, a background statement, a binary
// command or a redirect before any recognizer could run.
var bashppShellOperatorExprs = []struct {
	expr string
	op   string
}{
	{"true || false", "||"},
	{"true && false", "&&"},
	{"1 < 2", "<"},
	{"1 <= 2", "<="},
	{"1 > 2", ">"},
	{"1 >= 2", ">="},
	{"1 | 2", "|"},
	{"1 & 2", "&"},
	{"1 &^ 2", "&^"},
	{"1 << 2", "<<"},
	{"1 >> 2", ">>"},
}

func TestBashPPScalarExpressionShellOperatorsReachTheTypedTree(t *testing.T) {
	for _, test := range bashppShellOperatorExprs {
		t.Run(test.expr, func(t *testing.T) {
			src := "func main() {\n\tx := " + test.expr + "\n}\n"
			parse := func(bytewise bool) (*File, error) {
				var rd io.Reader = strings.NewReader(src)
				if bytewise {
					rd = bashppByteReader{rd}
				}
				return NewParser(Variant(LangBashPP)).Parse(rd, "expr.bpp")
			}
			buffered, bufferedErr := parse(false)
			streamed, streamedErr := parse(true)
			if fmt.Sprint(streamedErr) != fmt.Sprint(bufferedErr) || !reflect.DeepEqual(streamed, buffered) {
				t.Fatalf("one-byte parse differs:\nbuffered: %#v, %v\nstreamed: %#v, %v",
					buffered, bufferedErr, streamed, streamedErr)
			}
			if bufferedErr != nil {
				t.Fatalf("%v", bufferedErr)
			}
			fn := buffered.Stmts[0].Cmd.(*BashPPFuncDecl)
			if len(fn.Body.Stmts) != 1 {
				t.Fatalf("body has %d statements, want 1", len(fn.Body.Stmts))
			}
			stmt := fn.Body.Stmts[0]
			if len(stmt.Redirs) != 0 || stmt.Background {
				t.Fatalf("statement kept shell effects: redirs=%d background=%v",
					len(stmt.Redirs), stmt.Background)
			}
			decl, ok := stmt.Cmd.(*BashPPShortDecl)
			if !ok || decl.Expr == nil || decl.Rhs != nil {
				t.Fatalf("declaration = %#v", stmt.Cmd)
			}
			binary, ok := decl.Expr.(*BashPPBinaryExpr)
			if !ok {
				t.Fatalf("expression = %T, want *BashPPBinaryExpr", decl.Expr)
			}
			if binary.Op.Value != test.op {
				t.Fatalf("operator = %q, want %q", binary.Op.Value, test.op)
			}
			// Every position must still name the byte it was written at, which
			// is what lets a diagnostic point into the original source.
			start := strings.Index(src, test.expr)
			for _, sub := range []struct {
				name string
				node Node
				text string
				at   int
			}{
				{"expression", binary, test.expr, start},
				{"left", binary.X, test.expr[:strings.Index(test.expr, " ")], start},
				{"operator", binary.Op, test.op, start + strings.Index(test.expr, " ") + 1},
				{"right", binary.Y, test.expr[strings.LastIndex(test.expr, " ")+1:],
					start + strings.LastIndex(test.expr, " ") + 1},
			} {
				if got := int(sub.node.Pos().Offset()); got != sub.at {
					t.Errorf("%s starts at offset %d, want %d", sub.name, got, sub.at)
				}
				if got, want := int(sub.node.End().Offset()), sub.at+len(sub.text); got != want {
					t.Errorf("%s ends at offset %d, want %d", sub.name, got, want)
				}
			}
			// The printer must put the source back, and the result must parse
			// to the same tree.
			var printed strings.Builder
			if err := NewPrinter().Print(&printed, buffered); err != nil {
				t.Fatal(err)
			}
			if printed.String() != src {
				t.Fatalf("printed %q, want %q", printed.String(), src)
			}
			reparsed, err := NewParser(Variant(LangBashPP)).Parse(strings.NewReader(printed.String()), "expr.bpp")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(reparsed, buffered) {
				t.Fatalf("reparse differs:\n%#v\n%#v", reparsed, buffered)
			}
		})
	}
}

// Enumerated top-level Go bindings own their complete expression; Classic
// grammars continue to parse the same bytes as shell commands and redirects.
func TestBashPPScalarExpressionOperatorsAtTopLevel(t *testing.T) {
	for _, test := range bashppShellOperatorExprs {
		t.Run(test.expr, func(t *testing.T) {
			bashppCheckIdentical(t, "command x := "+test.expr+"\n")
			src := "x := " + test.expr + "\n"
			for _, posix := range []bool{false, true} {
				for _, mode := range bashppReadModes {
					pp, err := bashppParseAs(LangBashPP, src, posix, mode.wrap)
					if err != nil {
						t.Fatal(err)
					}
					if len(pp.Stmts) != 1 || len(pp.Stmts[0].Redirs) != 0 || pp.Stmts[0].Background {
						t.Fatalf("Go binding retained shell operators: %#v", pp.Stmts)
					}
					d, ok := pp.Stmts[0].Cmd.(*BashPPShortDecl)
					if !ok {
						t.Fatalf("binding: %T", pp.Stmts[0].Cmd)
					}
					expr, ok := d.Expr.(*BashPPBinaryExpr)
					if !ok || expr.Op.Value != test.op {
						t.Fatalf("operator: %#v", d.Expr)
					}
				}
			}
		})
	}
}

// bashppStmtSummary describes a statement list without using positions, so a
// Go region's body can be compared against the same source parsed as ordinary
// shell even though the two sit at different offsets.
func bashppStmtSummary(stmts []*Stmt) string {
	return bashppSummarize(stmts, true)
}

// bashppShellShape is bashppStmtSummary without the command node's type, so
// that a Class-E node standing where bash has a CallExpr does not register as
// a change to the SHELL structure — the statement, redirect and background
// layout — which is what must not move.
func bashppShellShape(stmts []*Stmt) string {
	return bashppSummarize(stmts, false)
}

// bashppSummarize describes a statement list without using positions, so the
// same source can be compared across languages and nesting depths.
func bashppSummarize(stmts []*Stmt, withType bool) string {
	var b strings.Builder
	var walk func([]*Stmt)
	walk = func(ss []*Stmt) {
		for _, s := range ss {
			if withType {
				fmt.Fprintf(&b, "%T;", s.Cmd)
			}
			fmt.Fprintf(&b, "stmt redirs=%d bg=%v;", len(s.Redirs), s.Background)
			for _, rd := range s.Redirs {
				fmt.Fprintf(&b, "  redir %s %q;", rd.Op, bashppWordText(rd.Word))
			}
			if bin, ok := s.Cmd.(*BinaryCmd); ok {
				fmt.Fprintf(&b, "  [%s];", bin.Op)
				walk([]*Stmt{bin.X, bin.Y})
			}
		}
	}
	walk(stmts)
	return b.String()
}

// TestBashPPScalarExpressionCarrierLeavesShellCommands pins ordinary commands
// and redirections written inside a Go region: with no short-declaration head
// to arm the carrier, they must parse exactly as bash parses them.
func TestBashPPScalarExpressionCarrierLeavesShellCommands(t *testing.T) {
	for _, body := range []string{
		"echo hi > out.txt",
		"cat < in.txt",
		"sort < in.txt > out.txt",
		"grep x | wc -l",
		"sleep 1 &",
		"true && false",
		"true || false",
		"cat << EOF\nbody\nEOF",
	} {
		t.Run(body, func(t *testing.T) {
			bash, err := bashppParseAs(LangBash, body+"\n", false, bashppReadModes[0].wrap)
			if err != nil {
				t.Fatal(err)
			}
			want := bashppStmtSummary(bash.Stmts)
			for _, mode := range bashppReadModes {
				pp, err := bashppParseAs(LangBashPP, "func main() {\n"+body+"\n}\n", false, mode.wrap)
				if err != nil {
					t.Fatalf("%s: %v", mode.name, err)
				}
				got := bashppStmtSummary(pp.Stmts[0].Cmd.(*BashPPFuncDecl).Body.Stmts)
				if got != want {
					t.Fatalf("%s: body parsed as %s, want %s", mode.name, got, want)
				}
			}
		})
	}
}

// TestBashPPScalarExpressionCarrierDeclinesNearMisses pins the shapes that do
// arm the carrier and must then be rolled back whole. Each keeps the shell
// effect — a redirect, a background `&`, an extra word — that the expression
// reading would have swallowed.
func TestBashPPScalarExpressionCarrierDeclinesNearMisses(t *testing.T) {
	for _, test := range []struct {
		body       string
		redirs     int
		background bool
	}{
		// A right-hand side that runs a command is not an exact-Go expression.
		{body: "out := $(echo 1) > f", redirs: 1},
		// The operator has no right-hand operand at all.
		{body: "x := 1 &", background: true},
		// The operand is a filename, not a Go scalar expression.
		{body: "x := 1 > out.txt", redirs: 1},
		// A trailing word the expression cannot absorb.
		{body: "x := 1 < 2 3", redirs: 1},
		// A digit glued to `<` is a file-descriptor redirect in every shell,
		// so the lexer never offers the carrier an operator token here.
		{body: "x := 1<2", redirs: 1},
		// A declaration head already carrying a redirect is left alone.
		{body: "x := 1 2> err.txt < in.txt", redirs: 2},
	} {
		t.Run(test.body, func(t *testing.T) {
			src := "func main() {\n\t" + test.body + "\n}\n"
			for _, mode := range bashppReadModes {
				f, err := bashppParseAs(LangBashPP, src, false, mode.wrap)
				if test.body != "x := 1 &" {
					pe, ok := err.(ParseError)
					if !ok || pe.Text != "invalid := binding" || pe.Pos.Line() != 2 {
						t.Fatalf("%s: expected positioned invalidbinding, got %v", mode.name, err)
					}
					continue
				}
				if err != nil {
					t.Fatalf("%s: %v", mode.name, err)
				}
				body := f.Stmts[0].Cmd.(*BashPPFuncDecl).Body
				if len(body.Stmts) != 1 {
					t.Fatalf("%s: body has %d statements, want 1", mode.name, len(body.Stmts))
				}
				stmt := body.Stmts[0]
				if len(stmt.Redirs) != test.redirs || stmt.Background != test.background {
					t.Fatalf("%s: redirs=%d background=%v, want %d and %v",
						mode.name, len(stmt.Redirs), stmt.Background, test.redirs, test.background)
				}
				Walk(f, func(node Node) bool {
					if binary, ok := node.(*BashPPBinaryExpr); ok {
						t.Errorf("%s: operator %q was claimed", mode.name, binary.Op.Value)
					}
					return true
				})
			}
		})
	}
}

func TestBashPPScalarExpressionExactPositions(t *testing.T) {
	const src = "func main() {\n\tx := 1  +\t2\n}\n"
	f, err := NewParser(Variant(LangBashPP)).Parse(bashppByteReader{strings.NewReader(src)}, "expr.bpp")
	if err != nil {
		t.Fatal(err)
	}
	fn := f.Stmts[0].Cmd.(*BashPPFuncDecl)
	expr := fn.Body.Stmts[0].Cmd.(*BashPPShortDecl).Expr.(*BashPPBinaryExpr)
	for _, test := range []struct {
		name       string
		node       Node
		text       string
		fromOffset int
	}{
		{"left", expr.X, "1", 0},
		{"operator", expr.Op, "+", 0},
		{"right", expr.Y, "2", strings.Index(src, "+") + 1},
	} {
		start := strings.Index(src[test.fromOffset:], test.text) + test.fromOffset
		if got := int(test.node.Pos().Offset()); got != start {
			t.Errorf("%s starts at offset %d, want %d", test.name, got, start)
		}
		if got, want := int(test.node.End().Offset()), start+len(test.text); got != want {
			t.Errorf("%s ends at offset %d, want %d", test.name, got, want)
		}
	}
}

func TestBashPPScalarExpressionClaimsTopLevelBinding(t *testing.T) {
	for _, mode := range bashppReadModes {
		f, err := bashppParseAs(LangBashPP, "x := 1 + 2\n", false, mode.wrap)
		if err != nil {
			t.Fatal(err)
		}
		d, ok := f.Stmts[0].Cmd.(*BashPPShortDecl)
		if !ok {
			t.Fatalf("binding: %T", f.Stmts[0].Cmd)
		}
		if _, ok := d.Expr.(*BashPPBinaryExpr); !ok {
			t.Fatalf("expression: %T", d.Expr)
		}
	}
}

func TestBashPPGoCallsAndTerminators(t *testing.T) {
	for _, src := range []string{
		"f(1, 2)", "x.y.z()", "clear(m)",
		"f(1); cat", "f(1)\ncat",
	} {
		t.Run(src, func(t *testing.T) {
			f, err := NewParser(Variant(LangBashPP)).Parse(strings.NewReader(src), "call.sh")
			if err != nil {
				t.Fatal(err)
			}
			found := false
			Walk(f, func(n Node) bool {
				if _, ok := n.(*BashPPCall); ok {
					found = true
				}
				return true
			})
			if !found {
				t.Fatalf("no BashPPCall in %T", f.Stmts[0].Cmd)
			}
		})
	}
}

func TestBashPPCallTerminatorGrammar(t *testing.T) {
	for _, tok := range []token{_EOF, _Newl, semicolon} {
		if !bashppCallTerminator(tok) {
			t.Errorf("published terminator %v was rejected", tok)
		}
	}
	for _, tok := range []token{
		and, or, orAnd, andAnd, orOr, andPipe, andBang,
		rdrOut, appOut, rdrIn, rdrInOut, dplIn, dplOut, rdrClob,
		rdrAll, appAll, rightParen,
	} {
		if bashppCallTerminator(tok) {
			t.Errorf("continuation %v was admitted as a call terminator", tok)
		}
	}
}

func TestBashPPParenFallbackIsExact(t *testing.T) {
	for _, src := range []string{
		"x.y() { echo ok; }", "x := f(1", "x := f(1 2)", "x := f(1,)",
		"f(1)>out", "f(1) >out", "x := f()>out", "x := f() >out",
		"f(1)<in", "f(1)>>out", "f(1)|&cat", "f(1)&&cat", "f(1)||cat",
		"f(1)& cat",
	} {
		t.Run(src, func(t *testing.T) {
			for _, posix := range []bool{false, true} {
				for _, bytewise := range []bool{false, true} {
					parse := func(lang LangVariant) (*File, error) {
						var rd io.Reader = strings.NewReader(src)
						if bytewise {
							rd = bashppByteReader{rd}
						}
						return NewParser(Variant(lang), PosixMode(posix)).Parse(rd, "fallback.sh")
					}
					bashFile, bashErr := parse(LangBash)
					ppFile, ppErr := parse(LangBashPP)
					if fmt.Sprint(ppErr) != fmt.Sprint(bashErr) {
						t.Fatalf("bytewise=%v diagnostic differs:\nbash: %v\npp:   %v", bytewise, bashErr, ppErr)
					}
					if !reflect.DeepEqual(ppFile, bashFile) {
						t.Fatalf("posix=%v bytewise=%v partial tree differs after rollback", posix, bytewise)
					}
				}
			}
		})
	}
}

// A pipe continues the command; it is not a statement terminator in the
// published Bash++ call grammar. The speculative call probe must therefore
// disappear completely, including with a reader that exposes one byte at a
// time, so the classic Bash tree and diagnostic remain byte-for-byte exact.
func TestBashPPCallBeforePipeFallsBackExactly(t *testing.T) {
	const src = "f(1)|cat"
	for _, posix := range []bool{false, true} {
		for _, bytewise := range []bool{false, true} {
			parse := func(lang LangVariant) (*File, error) {
				var rd io.Reader = strings.NewReader(src)
				if bytewise {
					rd = bashppByteReader{rd}
				}
				return NewParser(Variant(lang), PosixMode(posix)).Parse(rd, "pipe.sh")
			}
			bashFile, bashErr := parse(LangBash)
			ppFile, ppErr := parse(LangBashPP)
			if fmt.Sprint(ppErr) != fmt.Sprint(bashErr) {
				t.Fatalf("posix=%v bytewise=%v diagnostic differs:\nbash: %v\npp:   %v", posix, bytewise, bashErr, ppErr)
			}
			if !reflect.DeepEqual(ppFile, bashFile) {
				t.Fatalf("posix=%v bytewise=%v partial tree differs after rollback", posix, bytewise)
			}
			Walk(ppFile, func(n Node) bool {
				if _, ok := n.(*BashPPCall); ok {
					t.Fatalf("posix=%v bytewise=%v pipe prefix was claimed as BashPPCall", posix, bytewise)
				}
				return true
			})
		}
	}
}

func TestBashPPShortPorousShapesStayShell(t *testing.T) {
	for _, src := range []string{"x := x-y{}", "x := Max[]", "x := Max[int]junk"} {
		t.Run(src, func(t *testing.T) { bashppCheckDiagnostic(t, src, "invalid := binding") })
	}
}

func TestBashPPClassRArgumentsRemainUnevaluated(t *testing.T) {
	f, err := NewParser(Variant(LangBashPP)).Parse(strings.NewReader(`f("$x", $(side_effect), plain)`), "")
	if err == nil {
		t.Fatal("unsupported argument forms must roll back, not be evaluated or claimed")
	}
	f, err = NewParser(Variant(LangBashPP)).Parse(strings.NewReader(`f("literal", plain)`), "")
	if err != nil {
		t.Fatal(err)
	}
	call := f.Stmts[0].Cmd.(*BashPPCall)
	if _, ok := call.Args[0].Parts[0].(*DblQuoted); !ok {
		t.Fatalf("quoted argument was flattened to %T", call.Args[0].Parts[0])
	}
}
