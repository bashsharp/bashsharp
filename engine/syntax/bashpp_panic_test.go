// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package syntax

import (
	"io"
	"strings"
	"testing"
)

// `panic` and `recover` are Go's PREDECLARED functions, so a call to one is
// callable without a declaration. That matters to the parser for exactly one
// shape: the zero-argument `recover()`, which is also the prefix of a classic
// shell function definition and is therefore claimed only for a name already
// known to be callable — see [Parser.bashppCallable].
//
// The claim is bounded on both sides, and these tests are the boundary. What
// is claimed is `recover()` followed by a statement end, which bash rejects
// today. What is NOT claimed is `recover() { … }`, which bash accepts and
// which must keep meaning the shell function it has always meant.

func TestBashPPPredeclaredCallGrammar(t *testing.T) {
	t.Parallel()
	const src = "panic(\"boom\")\nrecover()\nv := recover()\nfunc f() {\n\tdefer recover()\n\tpanic(v)\n}\n"
	for _, rd := range []io.Reader{strings.NewReader(src), funcOneByteReader{strings.NewReader(src)}} {
		f, err := NewParser(Variant(LangBashPP)).Parse(rd, "panic.bpp")
		if err != nil {
			t.Fatal(err)
		}
		panicCall, ok := f.Stmts[0].Cmd.(*BashPPCall)
		if !ok || len(panicCall.Fun) != 1 || panicCall.Fun[0].Value != "panic" || len(panicCall.Args) != 1 {
			t.Fatalf("panic call = %#v", f.Stmts[0].Cmd)
		}
		recoverCall, ok := f.Stmts[1].Cmd.(*BashPPCall)
		if !ok || len(recoverCall.Fun) != 1 || recoverCall.Fun[0].Value != "recover" || len(recoverCall.Args) != 0 {
			t.Fatalf("recover call = %#v", f.Stmts[1].Cmd)
		}
		short, ok := f.Stmts[2].Cmd.(*BashPPShortDecl)
		if !ok || short.Call == nil || short.Call.Fun[0].Value != "recover" {
			t.Fatalf("recover binding = %#v", f.Stmts[2].Cmd)
		}
		decl := f.Stmts[3].Cmd.(*BashPPFuncDecl)
		deferred, ok := decl.Body.Stmts[0].Cmd.(*BashPPDefer)
		if !ok || deferred.Call == nil || deferred.Call.Fun[0].Value != "recover" {
			t.Fatalf("deferred recover = %#v", decl.Body.Stmts[0].Cmd)
		}
		// Positions must survive, since the printer reconstructs from them.
		if got := recoverCall.Pos().Offset(); got != uint(strings.Index(src, "recover()")) {
			t.Fatalf("recover position = %d", got)
		}
		var out strings.Builder
		if err := NewPrinter().Print(&out, f); err != nil || out.String() != src {
			t.Fatalf("print = %q, %v", out.String(), err)
		}
	}
}

// TestBashPPPredeclaredNamesStayShellDefinitions is the other side of the
// claim: a body after the parenthesis rewinds the transaction, so a script
// defining a shell function of either name parses exactly as it does today.
func TestBashPPPredeclaredNamesStayShellDefinitions(t *testing.T) {
	t.Parallel()
	for _, src := range []string{
		"recover() { echo hi; }\n",
		"panic() { echo hi; }\n",
		"recover() ( echo hi )\n",
	} {
		for _, lang := range []LangVariant{LangBash, LangBashPP} {
			f, err := NewParser(Variant(lang)).Parse(strings.NewReader(src), "")
			if err != nil {
				t.Fatalf("%s %q: %v", lang, src, err)
			}
			if _, ok := f.Stmts[0].Cmd.(*FuncDecl); !ok {
				t.Fatalf("%s %q: parsed as %T", lang, src, f.Stmts[0].Cmd)
			}
		}
	}
}

// TestBashPPPredeclaredNamesAreClassicUnderOtherDialects proves the predeclared
// claim is dialect-gated: Classic Bash still rejects `recover()` with no body,
// exactly as it did before the name existed.
func TestBashPPPredeclaredNamesAreClassicUnderOtherDialects(t *testing.T) {
	t.Parallel()
	for _, lang := range []LangVariant{LangBash, LangPOSIX} {
		if _, err := NewParser(Variant(lang)).Parse(strings.NewReader("recover()\n"), ""); err == nil {
			t.Errorf("%s parsed a bare recover()", lang)
		}
	}
}
