// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package syntax_test

import (
	"bytes"
	"io"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/bashsharp/bashsharp/engine/syntax/typedjson"
)

func TestBashPPDecoratorsRoundTrip(t *testing.T) {
	for _, src := range []string{
		"@trace()\n@retry(n: 3, backoff: \"1s\")\nfunc deploy(env string) string { return env }\n",
		"@guard(effects: \"read,net\")\nfunc (c *Client) Fetch(url string) string { return url }\n",
		"@audit(); agentic func summarize(text string) string { return text }\n",
		"@trace()\nfunction backup() { :; }\n",
		"@trace()\nbackup() { :; }\n",
		"@pkg.trace(label)\nfunc f() {}\n",
		"@outer()\n# between\n\n@inner(value)\nfunc f() {}\n",
	} {
		t.Run(src, func(t *testing.T) {
			for _, reader := range []io.Reader{strings.NewReader(src), iotest.OneByteReader(strings.NewReader(src))} {
				file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP), syntax.KeepComments(true)).Parse(reader, "decorator.bpp")
				if err != nil {
					t.Fatal(err)
				}
				var decorators []*syntax.BashPPDecorator
				switch decl := file.Stmts[0].Cmd.(type) {
				case *syntax.BashPPFuncDecl:
					decorators = decl.Decorators
				case *syntax.FuncDecl:
					decorators = decl.Decorators
				default:
					t.Fatalf("got %T, want decorated function declaration", decl)
				}
				if len(decorators) == 0 || file.Stmts[0].Cmd.Pos() != decorators[0].Pos() {
					t.Fatalf("decorators or declaration position lost: %#v", decorators)
				}
				walked := 0
				syntax.Walk(file, func(node syntax.Node) bool {
					if _, ok := node.(*syntax.BashPPDecorator); ok {
						walked++
					}
					return true
				})
				if walked != len(decorators) {
					t.Fatalf("Walk saw %d decorators, want %d", walked, len(decorators))
				}

				var encoded bytes.Buffer
				if err := typedjson.Encode(&encoded, file); err != nil {
					t.Fatal(err)
				}
				restored, err := typedjson.Decode(&encoded)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(file, restored) {
					t.Fatal("typed JSON round trip changed decorator tree")
				}
				var printed bytes.Buffer
				if err := syntax.NewPrinter().Print(&printed, restored); err != nil {
					t.Fatal(err)
				}
				printedText := printed.String()
				reparsed, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP), syntax.KeepComments(true)).Parse(strings.NewReader(printedText), "decorator.bpp")
				if err != nil {
					t.Fatalf("printed %q does not parse: %v", printedText, err)
				}
				var printedAgain bytes.Buffer
				if err := syntax.NewPrinter().Print(&printedAgain, reparsed); err != nil {
					t.Fatal(err)
				}
				if printedAgain.String() != printedText {
					t.Fatalf("print/parse round trip did not stabilize\nfirst: %q\nsecond: %q", printedText, printedAgain.String())
				}
			}
		})
	}
}

func TestBashPPDecoratorWholeStackFallback(t *testing.T) {
	for _, src := range []string{
		"@one()\n@two() { :; }\n",
		"@one(x)\n@two(y)\necho nope\n",
		"@one(x) echo nope\n",
		"@one(bad:)\nfunc f() {}\n",
	} {
		bash, bashErr := syntax.NewParser(syntax.Variant(syntax.LangBash), syntax.KeepComments(true)).Parse(strings.NewReader(src), "")
		bashpp, bashppErr := syntax.NewParser(syntax.Variant(syntax.LangBashPP), syntax.KeepComments(true)).Parse(strings.NewReader(src), "")
		if (bashErr == nil) != (bashppErr == nil) {
			t.Fatalf("%q: bash error %v, Bash++ error %v", src, bashErr, bashppErr)
		}
		if bashErr != nil && bashErr.Error() != bashppErr.Error() {
			t.Fatalf("%q: fallback diagnostic changed\nbash: %v\nBash++: %v", src, bashErr, bashppErr)
		}
		if bashErr == nil && !reflect.DeepEqual(bash, bashpp) {
			t.Fatalf("%q: whole-stack fallback changed the Bash tree", src)
		}
	}
}

func TestBashPPEmptyDecoratorCompoundLookahead(t *testing.T) {
	for _, body := range []string{
		"{ :; }", "( : )", "(( 1 ))", "[[ -n x ]]",
		"if true; then :; fi", "for x in a; do :; done",
		"while true; do break; done", "until false; do break; done",
		"case x in x) :;; esac", "select x in a; do break; done",
	} {
		src := "@n()\n# body\n" + body + "\n"
		file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP), syntax.KeepComments(true)).Parse(iotest.OneByteReader(strings.NewReader(src)), "")
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		decl, ok := file.Stmts[0].Cmd.(*syntax.FuncDecl)
		if !ok || decl.Name == nil || decl.Name.Value != "@n" || len(decl.Decorators) != 0 {
			t.Fatalf("%s: got %#v, want classic @n function declaration", body, file.Stmts[0].Cmd)
		}
	}
}

func TestBashPPDecoratorDialectIsolation(t *testing.T) {
	const src = "@trace()\nfunc f() {}\n"
	if _, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), ""); err != nil {
		t.Fatal(err)
	}
	for _, lang := range []syntax.LangVariant{syntax.LangBash, syntax.LangPOSIX, syntax.LangMirBSDKorn} {
		if _, err := syntax.NewParser(syntax.Variant(lang)).Parse(strings.NewReader(src), ""); err == nil {
			t.Fatalf("%s unexpectedly accepted decorator syntax", lang)
		}
	}
}

func TestBashPPDecoratorMinify(t *testing.T) {
	const src = "@outer()\n@inner(x)\nfunc f() {}\n"
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := syntax.NewPrinter(syntax.Minify(true)).Print(&out, file); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "@outer();") || !strings.Contains(out.String(), "@inner(x);") {
		t.Fatalf("minified decorators lost statement separators: %q", out.String())
	}
	if _, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(out.String()), ""); err != nil {
		t.Fatalf("minified decorators do not parse: %q: %v", out.String(), err)
	}
}
