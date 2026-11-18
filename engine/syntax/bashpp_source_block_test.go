package syntax

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
)

func TestBashPPSourceBlockPreservesLineContinuations(t *testing.T) {
	body := "#define TYPE_NAME(X) _Generic((X), \\\n    int: \"int\", \\\n    default: \"unknown\")\nint example(void) { return 0; }\n"
	src := "~~~c as c\n" + body + "~~~\nc.example()\n"
	for _, newline := range []string{"\n", "\r\n"} {
		input := strings.ReplaceAll(src, "\n", newline)
		for _, streamed := range []bool{false, true} {
			var reader interface{ Read([]byte) (int, error) } = strings.NewReader(input)
			if streamed {
				reader = iotest.OneByteReader(reader)
			}
			f, err := NewParser(Variant(LangBashPP)).Parse(reader, "continuation.bpp")
			if err != nil {
				t.Fatal(err)
			}
			block := f.Stmts[0].Cmd.(*SourceBlock)
			if block.Body != body || block.ClosingPos.Line() != 6 || f.Stmts[1].Pos().Line() != 7 {
				t.Fatalf("newline=%q streamed=%v: body=%q closing=%v next=%v", newline, streamed, block.Body, block.ClosingPos, f.Stmts[1].Pos())
			}
			var out bytes.Buffer
			if err := NewPrinter().Print(&out, f); err != nil {
				t.Fatal(err)
			}
			if out.String() != src {
				t.Fatalf("printed source = %q, want %q", out.String(), src)
			}
		}
	}
}

func TestBashPPSourceBlockParsePrintWalkAndStreaming(t *testing.T) {
	src := "~~~~python as py\n\ndef add(a: int, b: int) -> int:\n    return a + b\n~~~~\npy.add(2, 3)\n"
	parse := func(rd interface{ Read([]byte) (int, error) }) *File {
		t.Helper()
		f, err := NewParser(Variant(LangBashPP)).Parse(rd, "polyglot.bpp")
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	buffered := parse(strings.NewReader(src))
	streamed := parse(iotest.OneByteReader(strings.NewReader(src)))
	for _, f := range []*File{buffered, streamed} {
		if len(f.Stmts) != 2 {
			t.Fatalf("statements = %d", len(f.Stmts))
		}
		block, ok := f.Stmts[0].Cmd.(*SourceBlock)
		if !ok {
			t.Fatalf("command = %T", f.Stmts[0].Cmd)
		}
		if block.Fence != "~~~~" || block.Language.Value != "python" || block.Alias.Value != "py" {
			t.Fatalf("block = %#v", block)
		}
		if block.Body != "\ndef add(a: int, b: int) -> int:\n    return a + b\n" {
			t.Fatalf("body = %q", block.Body)
		}
		if block.Pos().Line() != 1 || block.BodyPos.Line() != 2 || block.ClosingPos.Line() != 5 {
			t.Fatalf("positions: %v %v %v", block.Pos(), block.BodyPos, block.ClosingPos)
		}
		seen := false
		Walk(f, func(n Node) bool {
			if n == block {
				seen = true
			}
			return true
		})
		if !seen {
			t.Fatal("source block not walked")
		}
	}
	var out bytes.Buffer
	if err := NewPrinter().Print(&out, buffered); err != nil {
		t.Fatal(err)
	}
	if out.String() != src {
		t.Fatalf("printed:\n%s", out.String())
	}
}

func TestBashPPSourceBlockDialectIsolationAndNearMiss(t *testing.T) {
	valid := "~~~python\ndef f():\n    return 1\n~~~\n"
	f, err := NewParser(Variant(LangBashPP)).Parse(strings.NewReader(valid), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := f.Stmts[0].Cmd.(*SourceBlock); !ok {
		t.Fatalf("command = %T", f.Stmts[0].Cmd)
	}
	for _, src := range []string{"~~python\n", "~~~python nope\n", "echo ~~~python\n", " ~~~python\n", "command ~~~python\n", "'~~~python'\n"} {
		f, err := NewParser(Variant(LangBashPP)).Parse(strings.NewReader(src), "")
		if err != nil {
			continue
		}
		Walk(f, func(n Node) bool {
			if _, ok := n.(*SourceBlock); ok {
				t.Fatalf("near miss claimed: %q", src)
			}
			return true
		})
	}
	for _, lang := range []LangVariant{LangBash, LangPOSIX} {
		f, err := NewParser(Variant(lang)).Parse(strings.NewReader(valid), "")
		if err != nil {
			continue
		}
		Walk(f, func(n Node) bool {
			if _, ok := n.(*SourceBlock); ok {
				t.Fatalf("%s claimed fence", lang)
			}
			return true
		})
	}
}

func TestBashPPSourceBlockUnclosed(t *testing.T) {
	_, err := NewParser(Variant(LangBashPP)).Parse(strings.NewReader("~~~python\ndef f(): pass\n"), "bad.bpp")
	if err == nil || !strings.Contains(err.Error(), "unclosed source block") {
		t.Fatalf("error = %v", err)
	}
}

func TestBashPPSourceBlockCRLFUsesParserNewlineContract(t *testing.T) {
	f, err := NewParser(Variant(LangBashPP)).Parse(strings.NewReader("~~~python\r\ndef f():\r\n    return 1\r\n~~~\r\n"), "windows.bpp")
	if err != nil {
		t.Fatal(err)
	}
	block := f.Stmts[0].Cmd.(*SourceBlock)
	if block.Body != "def f():\n    return 1\n" || block.ClosingPos.Line() != 4 {
		t.Fatalf("body=%q closing=%v", block.Body, block.ClosingPos)
	}
}

func TestBashPPTypeScriptSourceBlockRegistersDirectCalls(t *testing.T) {
	source := "~~~typescript\nexport function answer(): number { return 42 }\nfunction greet(name: string): string { return name }\n~~~\nx := answer()\ny := greet(hello)\n"
	f, err := NewParser(Variant(LangBashPP)).Parse(strings.NewReader(source), "typescript.bpp")
	if err != nil {
		t.Fatal(err)
	}
	block := f.Stmts[0].Cmd.(*SourceBlock)
	if block.Language.Value != "typescript" || len(f.Stmts) != 3 {
		t.Fatalf("block=%#v statements=%d", block, len(f.Stmts))
	}
}

func TestBashPPJavaScriptSourceBlockRegistersDirectCalls(t *testing.T) {
	for _, language := range []string{"javascript", "js"} {
		src := "~~~" + language + "\nimport path from \"node:path\"\nexport function answer() { return 42 }\nfunction greet(name) { return name }\n~~~\nx := answer()\ny := greet(hello)\n"
		f, err := NewParser(Variant(LangBashPP)).Parse(strings.NewReader(src), "javascript.bpp")
		if err != nil {
			t.Fatalf("%s: %v", language, err)
		}
		block, ok := f.Stmts[0].Cmd.(*SourceBlock)
		if !ok || block.Language.Value != language || len(f.Stmts) != 3 {
			t.Fatalf("%s: block=%#v statements=%d", language, f.Stmts[0].Cmd, len(f.Stmts))
		}
	}
}

func TestBashPPRustSourceBlockRegistersDirectCalls(t *testing.T) {
	for _, language := range []string{"rust", "rs"} {
		src := "~~~" + language + "\npub fn add(a: i64, b: i64) -> i64 { a + b }\n~~~\nx := add(1, 2)\n"
		f, err := NewParser(Variant(LangBashPP)).Parse(strings.NewReader(src), "rust.bpp")
		if err != nil {
			t.Fatalf("%s: %v", language, err)
		}
		block, ok := f.Stmts[0].Cmd.(*SourceBlock)
		if !ok || block.Language.Value != language {
			t.Fatalf("%s: block = %#v", language, f.Stmts[0].Cmd)
		}
		decl, ok := f.Stmts[1].Cmd.(*BashPPShortDecl)
		if !ok || decl.Call == nil || decl.Call.Fun[0].Value != "add" {
			t.Fatalf("%s: call = %#v", language, f.Stmts[1].Cmd)
		}
	}
}

func TestBashPPGoSourceBlockUsesReservedLanguageName(t *testing.T) {
	src := "~~~go as go\nfunc Answer() int { return 42 }\n~~~\nx := go.Answer()\n"
	f, err := bashppParse(LangBashPP, src)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Stmts) != 2 {
		t.Fatalf("statements = %d", len(f.Stmts))
	}
	if block, ok := f.Stmts[0].Cmd.(*SourceBlock); !ok || block.Language.Value != "go" || block.Alias.Value != "go" {
		t.Fatalf("first statement = %#v", f.Stmts[0].Cmd)
	}
}

func TestBashPPNativeSourceBlockRegistersDirectCalls(t *testing.T) {
	for _, language := range []string{"c", "cpp", "cxx"} {
		src := "~~~" + language + "\nlong long answer() { return 42; }\n~~~\nx := answer()\n"
		f, err := NewParser(Variant(LangBashPP)).Parse(strings.NewReader(src), "native.bpp")
		if err != nil {
			t.Fatalf("%s: %v", language, err)
		}
		if _, ok := f.Stmts[1].Cmd.(*BashPPShortDecl); !ok {
			t.Fatalf("%s: direct call = %#v", language, f.Stmts[1].Cmd)
		}
	}
}

func TestBashPPShellSourceBlockRegistersDirectCalls(t *testing.T) {
	for _, language := range []string{"bash", "sh"} {
		src := "~~~" + language + "\nAnswer() { printf 42; }\n~~~\nx := Answer()\n"
		f, err := NewParser(Variant(LangBashPP)).Parse(strings.NewReader(src), "shell-island.bpp")
		if err != nil {
			t.Fatalf("%s: %v", language, err)
		}
		if _, ok := f.Stmts[1].Cmd.(*BashPPShortDecl); !ok {
			t.Fatalf("%s: direct call = %#v", language, f.Stmts[1].Cmd)
		}
	}
}

// `py` is an alias spelling of `python` (as `ts` is of `typescript`): the
// parser's def look-ahead must treat a naked `~~~py` block like `~~~python`,
// so a later zero-argument `answer()` parses as a direct call rather than a
// shell function header.
func TestBashPPSourceBlockPyAliasDirectCallLookahead(t *testing.T) {
	for _, lang := range []string{"python", "py"} {
		src := "~~~" + lang + "\ndef answer() -> int:\n    return 42\n~~~\nx := answer()\necho \"$x\"\n"
		f, err := NewParser(Variant(LangBashPP)).Parse(strings.NewReader(src), "polyglot.bpp")
		if err != nil {
			t.Fatalf("%s: %v", lang, err)
		}
		if len(f.Stmts) != 3 {
			t.Fatalf("%s: statements = %d", lang, len(f.Stmts))
		}
		block, ok := f.Stmts[0].Cmd.(*SourceBlock)
		if !ok || block.Language.Value != lang {
			t.Fatalf("%s: block = %#v", lang, f.Stmts[0].Cmd)
		}
		if _, ok := f.Stmts[1].Cmd.(*FuncDecl); ok {
			t.Fatalf("%s: answer() parsed as a shell function header", lang)
		}
	}
}

func TestBashPPSourceBlockRunnerOverride(t *testing.T) {
	// The runner override, with and without an explicit alias, parses,
	// prints back byte-identical and walks the runner literal; the "as !R"
	// shorthand binds the alias to the runner's name.
	for src, want := range map[string][2]string{
		"~~~tf as iac !my-tofu\nresource \"x\" \"y\" {}\n~~~\n": {"iac", "my-tofu"},
		"~~~tf as !tofu\nx = 1\n~~~\n":                          {"tofu", "tofu"},
		"~~~dockerfile !img_runner\nFROM scratch\n~~~\n":        {"", "img_runner"},
		"~~~dockerfile as img\nFROM scratch\n~~~\n":             {"img", ""},
	} {
		f, err := NewParser(Variant(LangBashPP)).Parse(strings.NewReader(src), "fence.bsh")
		if err != nil {
			t.Fatalf("%q: %v", src, err)
		}
		block, ok := f.Stmts[0].Cmd.(*SourceBlock)
		if !ok {
			t.Fatalf("%q: command = %T", src, f.Stmts[0].Cmd)
		}
		alias, runner := "", ""
		if block.Alias != nil {
			alias = block.Alias.Value
		}
		if block.Runner != nil {
			runner = block.Runner.Value
			if block.Runner.Pos().Line() != 1 || block.Runner.Pos().Col() == 0 {
				t.Fatalf("%q: runner position %v", src, block.Runner.Pos())
			}
			if col := block.Runner.Pos().Col(); src[col-1] != runner[0] {
				t.Fatalf("%q: runner column %d points at %q", src, col, src[col-1:col])
			}
		}
		if alias != want[0] || runner != want[1] {
			t.Fatalf("%q: alias=%q runner=%q, want %v", src, alias, runner, want)
		}
		walked := false
		Walk(f, func(n Node) bool {
			if lit, ok := n.(*Lit); ok && block.Runner != nil && lit == block.Runner {
				walked = true
			}
			return true
		})
		if block.Runner != nil && !walked {
			t.Fatalf("%q: runner literal not walked", src)
		}
		var out bytes.Buffer
		if err := NewPrinter().Print(&out, f); err != nil {
			t.Fatal(err)
		}
		if out.String() != src {
			t.Fatalf("%q printed as %q", src, out.String())
		}
	}
	// A shorthand whose runner name is not a valid alias is a near miss, not
	// a fence: the shape needs an explicit alias.
	f, err := NewParser(Variant(LangBashPP)).Parse(strings.NewReader("~~~tf as !my-tofu\n"), "")
	if err == nil {
		Walk(f, func(n Node) bool {
			if _, ok := n.(*SourceBlock); ok {
				t.Fatal("as !my-tofu claimed as a fence")
			}
			return true
		})
	}
}

// Sprint 473 #2395: an opener of 64 or more tildes carries its language past
// maxLookahead, so truncating before the fence check read it as bare tildes
// and fell back to shell. Any width of three or more must open, and the
// first exact closer wins: a different-width tilde line stays body text,
// even a second line exactly equal to the fence after the block closes.
func TestBashPPSourceBlockWideFenceFirstExactCloserWins(t *testing.T) {
	for _, width := range []int{3, 7, 63, 64, 200} {
		fence := strings.Repeat("~", width)
		src := fence + "python\n" +
			"def hi():\n" +
			"    return 1\n" +
			"~~~~\n" +
			fence + "\n" +
			"x := hi()\n" +
			fence + "\n"
		f, err := NewParser(Variant(LangBashPP)).Parse(strings.NewReader(src), "wide.bsh")
		if err != nil {
			t.Fatalf("width %d: %v", width, err)
		}
		if len(f.Stmts) != 3 {
			t.Fatalf("width %d: statements = %d", width, len(f.Stmts))
		}
		block, ok := f.Stmts[0].Cmd.(*SourceBlock)
		if !ok {
			t.Fatalf("width %d: command = %T", width, f.Stmts[0].Cmd)
		}
		if block.Fence != fence || block.Language.Value != "python" {
			t.Fatalf("width %d: fence = %q language = %q", width, block.Fence, block.Language.Value)
		}
		if want := "def hi():\n    return 1\n~~~~\n"; block.Body != want {
			t.Fatalf("width %d: body = %q, want %q", width, block.Body, want)
		}
		if _, ok := f.Stmts[1].Cmd.(*BashPPShortDecl); !ok {
			t.Fatalf("width %d: following call = %#v", width, f.Stmts[1].Cmd)
		}
		if _, ok := f.Stmts[2].Cmd.(*SourceBlock); ok {
			t.Fatalf("width %d: trailing bare fence reopened a block", width)
		}
	}
}

// `embed TYPE "PATH" …` is the file's bytes written as an inline fence, the
// path relative to the parsed file's directory; other shapes stay commands.
func TestBashPPEmbed(t *testing.T) {
	dir := t.TempDir()
	body := "def add(a: int, b: int) -> int:\n    return a + b\n~~~\n"
	if err := os.WriteFile(filepath.Join(dir, "m.py"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	parse := func(src string) (*File, error) {
		return NewParser(Variant(LangBashPP)).Parse(strings.NewReader(src), filepath.Join(dir, "s.bsh"))
	}
	src := "embed python \"./m.py\" as py\npy.add(2, 3)\nembed notes \"./m.py\" as !tally\n"
	f, err := parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Stmts) != 3 {
		t.Fatalf("statements = %d", len(f.Stmts))
	}
	block, ok := f.Stmts[0].Cmd.(*SourceBlock)
	if !ok || block.Src.Value != "./m.py" || block.Language.Value != "python" || block.Alias.Value != "py" || block.Body != body {
		t.Fatalf("block = %#v", f.Stmts[0].Cmd)
	}
	runner, ok := f.Stmts[2].Cmd.(*SourceBlock)
	if !ok || runner.Alias.Value != "tally" || runner.Runner.Value != "tally" {
		t.Fatalf("runner block = %#v", f.Stmts[2].Cmd)
	}
	var out bytes.Buffer
	if err := NewPrinter().Print(&out, f); err != nil {
		t.Fatal(err)
	}
	if out.String() != src {
		t.Fatalf("printed:\n%s", out.String())
	}
	for _, miss := range []string{"embed foo bar\n", "embed py ./m.py as m\n", "embed py \"m.py\"\n", "embed py \"./$x\"\n", "  embed py \"./m.py\"\n", "embed py \"./m.py\" > out\n"} {
		f, err := parse(miss)
		if err != nil {
			t.Fatalf("%q: %v", miss, err)
		}
		if _, ok := f.Stmts[0].Cmd.(*SourceBlock); ok {
			t.Fatalf("%q claimed as embed", miss)
		}
	}
	if _, err := parse("embed python \"./missing.py\" as py\n"); err == nil || !strings.Contains(err.Error(), "missing.py") {
		t.Fatalf("missing file: err = %v", err)
	}
}
