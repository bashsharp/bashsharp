// Copyright (c) 2017, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package syntax

import (
	"strings"
	"testing"
)

// PosixMode must apply POSIX behavioral parse rules (single quotes literal
// inside ${...}) ON TOP OF LangBash, WITHOUT dropping bash grammar extensions
// like ${a:off:len} substrings — mirroring `bash --posix`.
func TestPosixModeKeepsBashExtensions(t *testing.T) {
	t.Parallel()
	// A bash extension that LangPOSIX rejects: substring expansion.
	const src = `a=hello; echo "${a:1:3}"`
	mustParse := func(opts ...ParserOption) {
		t.Helper()
		if _, err := NewParser(opts...).Parse(strings.NewReader(src), ""); err != nil {
			t.Fatalf("parse failed for %q: %v", src, err)
		}
	}
	mustReject := func(opts ...ParserOption) {
		t.Helper()
		if _, err := NewParser(opts...).Parse(strings.NewReader(src), ""); err == nil {
			t.Fatalf("expected parse error for %q under strict POSIX", src)
		}
	}
	mustParse(Variant(LangBash))                  // bash: ok
	mustParse(Variant(LangBash), PosixMode(true)) // bash --posix: STILL ok (the fix)
	mustReject(Variant(LangPOSIX))                // strict posix grammar: rejected
}

// bash --posix keeps the bash extension allowing non-POSIX function names
// (hyphens, dots): `test-hyphen() {}` runs under `bash --posix`. Only the pure
// LangPOSIX grammar restricts function names to POSIX "names".
func TestPosixModeAllowsNonPosixFuncNames(t *testing.T) {
	t.Parallel()
	const src = `test-hyphen() { :; }`
	parse := func(opts ...ParserOption) error {
		_, err := NewParser(opts...).Parse(strings.NewReader(src), "")
		return err
	}
	if err := parse(Variant(LangBash)); err != nil {
		t.Fatalf("bash: %v", err)
	}
	if err := parse(Variant(LangBash), PosixMode(true)); err != nil {
		t.Fatalf("bash --posix should accept hyphen func names: %v", err)
	}
	if err := parse(Variant(LangPOSIX)); err == nil {
		t.Fatal("LangPOSIX should reject a hyphen func name")
	}
}

func TestPosixModeAllowsExportReadonlyFunctionNames(t *testing.T) {
	parse := func(src string) error {
		p := NewParser(Variant(LangBash), PosixMode(true))
		_, err := p.Parse(strings.NewReader(src), "")
		return err
	}
	for _, src := range []string{
		"export() { :; }\n",
		"readonly() { :; }\n",
	} {
		if err := parse(src); err != nil {
			t.Fatalf("bash --posix should accept %q as a function declaration: %v", src, err)
		}
	}
}

func TestLineContinuationInsideOperators(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		src   string
		check func(*testing.T, *File)
	}{
		{"reserved word", "i\\\nf true; then :; fi", func(t *testing.T, file *File) {
			if _, ok := file.Stmts[0].Cmd.(*IfClause); !ok {
				t.Fatalf("wanted IfClause, got %T", file.Stmts[0].Cmd)
			}
		}},
		{"and operator", ": &\\\n& :", func(t *testing.T, file *File) {
			bin, ok := file.Stmts[0].Cmd.(*BinaryCmd)
			if !ok {
				t.Fatalf("wanted BinaryCmd, got %T", file.Stmts[0].Cmd)
			}
			if bin.Op != AndStmt {
				t.Fatalf("wanted && operator, got %s", bin.Op)
			}
		}},
		{"or operator", ": |\\\n| :", func(t *testing.T, file *File) {
			bin, ok := file.Stmts[0].Cmd.(*BinaryCmd)
			if !ok {
				t.Fatalf("wanted BinaryCmd, got %T", file.Stmts[0].Cmd)
			}
			if bin.Op != OrStmt {
				t.Fatalf("wanted || operator, got %s", bin.Op)
			}
		}},
		{"append redirection", ": >\\\n> out", func(t *testing.T, file *File) {
			redirs := file.Stmts[0].Redirs
			if len(redirs) != 1 || redirs[0].Op != AppOut {
				t.Fatalf("wanted >> redirect, got %#v", redirs)
			}
			if got := redirs[0].Word.Lit(); got != "out" {
				t.Fatalf("redirect word = %q, want out", got)
			}
		}},
		{"clobber redirection", ": >\\\n| out", func(t *testing.T, file *File) {
			redirs := file.Stmts[0].Redirs
			if len(redirs) != 1 || redirs[0].Op != RdrClob {
				t.Fatalf("wanted >| redirect, got %#v", redirs)
			}
			if got := redirs[0].Word.Lit(); got != "out" {
				t.Fatalf("redirect word = %q, want out", got)
			}
		}},
		{"duplicate input redirection", ": <\\\n& 0", func(t *testing.T, file *File) {
			redirs := file.Stmts[0].Redirs
			if len(redirs) != 1 || redirs[0].Op != DplIn {
				t.Fatalf("wanted <& redirect, got %#v", redirs)
			}
			if got := redirs[0].Word.Lit(); got != "0" {
				t.Fatalf("redirect word = %q, want 0", got)
			}
		}},
		{"duplicate output redirection", ": >\\\n& 1", func(t *testing.T, file *File) {
			redirs := file.Stmts[0].Redirs
			if len(redirs) != 1 || redirs[0].Op != DplOut {
				t.Fatalf("wanted >& redirect, got %#v", redirs)
			}
			if got := redirs[0].Word.Lit(); got != "1" {
				t.Fatalf("redirect word = %q, want 1", got)
			}
		}},
		{"heredoc operator", "cat <\\\n<EOF\nbody\nEOF\n", func(t *testing.T, file *File) {
			redirs := file.Stmts[0].Redirs
			if len(redirs) != 1 || redirs[0].Op != Hdoc {
				t.Fatalf("wanted << redirect, got %#v", redirs)
			}
			if got := redirs[0].Word.Lit(); got != "EOF" {
				t.Fatalf("heredoc word = %q, want EOF", got)
			}
		}},
		{"parameter operator", "echo ${a:\\\n-1}", func(t *testing.T, file *File) {
			call := file.Stmts[0].Cmd.(*CallExpr)
			pe := call.Args[1].Parts[0].(*ParamExp)
			if pe.Exp == nil || pe.Exp.Op != DefaultUnsetOrNull {
				t.Fatalf("wanted :- expansion, got %#v", pe.Exp)
			}
			if got := pe.Exp.Word.Lit(); got != "1" {
				t.Fatalf("expansion word = %q, want 1", got)
			}
		}},
		{"arithmetic operator", "echo $((1 >\\\n> 1))", func(t *testing.T, file *File) {
			call := file.Stmts[0].Cmd.(*CallExpr)
			arith := call.Args[1].Parts[0].(*ArithmExp)
			bin, ok := arith.X.(*BinaryArithm)
			if !ok {
				t.Fatalf("wanted BinaryArithm, got %T", arith.X)
			}
			if bin.Op != Shr {
				t.Fatalf("wanted >> arithmetic operator, got %s", bin.Op)
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			file, err := NewParser(Variant(LangBash)).Parse(strings.NewReader(tc.src), "")
			if err != nil {
				t.Fatalf("parse failed: %v", err)
			}
			tc.check(t, file)
		})
	}
}

func TestPosixModeParamExpSingleQuotesLiteral(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		`echo "${a+'x'}"`:   "'x'",
		`echo "${a+$'\t'}"`: "$'\\t'",
		`echo "${a-'x'}"`:   "'x'",
		`echo "${a-$'\t'}"`: "$'\\t'",
		`echo "${a='x'}"`:   "'x'",
		`echo "${a=$'\t'}"`: "$'\\t'",
	}
	for src, want := range tests {
		t.Run(src, func(t *testing.T) {
			t.Parallel()
			file, err := NewParser(Variant(LangBash), PosixMode(true)).Parse(strings.NewReader(src), "")
			if err != nil {
				t.Fatalf("parse failed: %v", err)
			}
			call := file.Stmts[0].Cmd.(*CallExpr)
			dq := call.Args[1].Parts[0].(*DblQuoted)
			pe := dq.Parts[0].(*ParamExp)
			if got := pe.Exp.Word.Lit(); got != want {
				t.Fatalf("single quotes should be literal text, want %q, got %q", want, got)
			}
		})
	}
}
