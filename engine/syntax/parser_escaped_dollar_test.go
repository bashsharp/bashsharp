package syntax

import (
	"strings"
	"testing"
)

// An escaped dollar before a parenthesis in an assignment value leaves a bare
// `(` after the assignment, which bash rejects with a syntax error. Bash++
// used to slice an empty argument list there and panic.
func TestParseEscapedDollarParenAssign(t *testing.T) {
	t.Parallel()
	const wantMsg = "a command can only contain words and redirects; encountered `(`"
	langs := []LangVariant{LangBash, LangPOSIX, LangMirBSDKorn, LangBats, LangBashPP}
	for _, lang := range langs {
		t.Run(lang.String(), func(t *testing.T) {
			_, err := NewParser(Variant(lang)).Parse(strings.NewReader("h=\\$(echo z)\n"), "")
			if err == nil {
				t.Fatal("expected a syntax error")
			}
			if want := "1:5: " + wantMsg; err.Error() != want {
				t.Fatalf("error mismatch\nwant: %s\ngot:  %s", want, err)
			}
		})
	}
}

func TestParseEscapedDollarParenNoPanic(t *testing.T) {
	t.Parallel()
	inputs := []string{
		"h=\\$(echo z)", "h=\\$(echo z)\n", "h=\\$(", "h=\\$((", "h=\\$(()", "h=\\$((1))",
		"a=b\\$(x)", "a=b\\$(x) c", "x h=\\$(echo z)", "h+=\\$(echo z)", "h[1]=\\$(echo z)",
		"h=\\$( echo z )", "h=\\\\$(echo z)", "h=\\${x}(", "h=\\`(a)", "h=\\$(a)b",
		"h=\\$(echo z) x", "h=\\$(echo z);", "h=\\$(echo z) | cat", "h=\\$(echo z) >f",
		"h='x'\\$(y)", "h=\"x\"\\$(y)", "h=\\$(echo z)\\\n", "a=1 b=\\$(c) d=2",
		"export h=\\$(echo z)", "local h=\\$(echo z)", "h=(\\$(echo z))", "h=\\$[1](",
		"h=\\(", "h=\\$ (", "f() { h=\\$(echo z); }", "if h=\\$(x); then :; fi",
		"h=\\$(echo z) &&\n", "$(a)", "h= (x)", "h=(", "a=b (c)", "a=b\\(",
	}
	langs := []LangVariant{LangBash, LangPOSIX, LangMirBSDKorn, LangBats, LangZsh, LangBashPP}
	for _, lang := range langs {
		t.Run(lang.String(), func(t *testing.T) {
			for _, in := range inputs {
				func() {
					defer func() {
						if r := recover(); r != nil {
							t.Errorf("panic on %q: %v", in, r)
						}
					}()
					NewParser(Variant(lang)).Parse(strings.NewReader(in), "")
				}()
			}
		})
	}
}
