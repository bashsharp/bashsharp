// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package syntax

import (
	"strings"
	"testing"
)

// The inbound typed-capture spellings from the ratified P5 table need no new
// grammar: `r, err := run("ls")`, `out, err := capture(ls)` and
// `v, err := json.Decode(out)` all ride the existing `:=`-with-call form.
// These tests pin that the EXISTING grammar claims them under LangBashPP —
// so the interpreter's capture hooks are reachable from source — and that
// every other dialect keeps rejecting them exactly as stock bash does
// (Class R: the spelling is already a bash syntax error).

func TestBashPPCaptureShapesParse(t *testing.T) {
	t.Parallel()
	tests := []struct {
		src      string
		funParts int
		args     int
	}{
		{`r, err := run("ls")`, 1, 1},
		{`out, err := capture(ls)`, 1, 1},
		{`out, err := capture("head", "-c", "100", "/dev/zero")`, 1, 4},
		{`out, err := capture(args...)`, 1, 1},
		{`v, err := json.Decode(out)`, 2, 1},
	}
	for _, tc := range tests {
		f, err := NewParser(Variant(LangBashPP)).Parse(strings.NewReader(tc.src), "")
		if err != nil {
			t.Fatalf("%s: %v", tc.src, err)
		}
		d, ok := f.Stmts[0].Cmd.(*BashPPShortDecl)
		if !ok || d.Call == nil {
			t.Fatalf("%s: got %T, want a BashPPShortDecl carrying a call", tc.src, f.Stmts[0].Cmd)
		}
		if len(d.Call.Fun) != tc.funParts || len(d.Call.Args) != tc.args {
			t.Fatalf("%s: Fun=%d Args=%d, want Fun=%d Args=%d",
				tc.src, len(d.Call.Fun), len(d.Call.Args), tc.funParts, tc.args)
		}
	}
}

// The capture spellings are Class R: stock bash rejects them, and so must
// every non-Bash++ dialect here — never quietly claimed, never re-meaning'd.
// The names themselves stay ordinary words everywhere.
func TestBashPPCaptureShapesInertOutsideBashPP(t *testing.T) {
	t.Parallel()
	for _, lang := range []LangVariant{LangBash, LangPOSIX, LangMirBSDKorn} {
		for _, src := range []string{
			`r, err := run("ls")`,
			`out, err := capture(ls)`,
			`v, err := json.Decode(out)`,
		} {
			if _, err := NewParser(Variant(lang)).Parse(strings.NewReader(src), ""); err == nil {
				t.Fatalf("%v accepted %q; the capture form must remain a syntax error outside bash++", lang, src)
			}
		}
		// As ordinary shell words the names parse exactly as before: a
		// function definition and plain command arguments.
		f, err := NewParser(Variant(lang)).Parse(
			strings.NewReader("run() { echo x; }\ncapture run\n"), "")
		if err != nil {
			t.Fatalf("%v: %v", lang, err)
		}
		if _, ok := f.Stmts[0].Cmd.(*FuncDecl); !ok {
			t.Fatalf("%v: run() {…} parsed as %T, want a shell FuncDecl", lang, f.Stmts[0].Cmd)
		}
		if _, ok := f.Stmts[1].Cmd.(*CallExpr); !ok {
			t.Fatalf("%v: `capture run` parsed as %T, want a plain CallExpr", lang, f.Stmts[1].Cmd)
		}
	}
}
