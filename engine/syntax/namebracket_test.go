// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

package syntax

import (
	"bytes"
	"strings"
	"testing"
	"testing/iotest"
)

// TestNameBracketCommandPositionMatchesBash pins the fix for the
// command-position NAME[...] divergence.
//
// Bash treats NAME[...] specially only when it is the target of an ASSIGNMENT.
// Without a following '=', `f[int]` is an ordinary word and the brackets are a
// glob character class. The parser used to commit to the assignment path on the
// '[' alone, which sent the subscript through the ARITHMETIC parser — so shapes
// whose subscript is not valid arithmetic became hard parse errors where real
// bash 5.3 accepts them as words.
//
// The expectations below were measured against GNU bash 5.3.15 with `bash -n`,
// in both default and --posix mode; every one of them behaves identically in
// the two modes, which is why the table carries a single verdict per shape.
// The divergence reproduced under --posix, so it reached the certification
// profile — that is why this is a fidelity fix and not a Bash++ concern.
func TestNameBracketCommandPositionMatchesBash(t *testing.T) {
	tests := []struct {
		src     string
		wantErr bool
		why     string
	}{
		// The five reported divergences: subscripts that are not valid
		// arithmetic. Real bash parses each as an ordinary glob word.
		{`f[[]int]`, false, "glob word, not an assignment"},
		{`f[map[string]int]`, false, "glob word, nested brackets"},
		{`f[*T]`, false, "glob word, '*' is not an arithmetic operand here"},
		{`f[[]]`, false, "glob word, empty inner brackets"},
		{`f[[x]]`, false, "glob word, nested brackets"},

		// Real assignments must keep working. These are the regression risk of
		// the fix: if the lookahead wrongly reports "no assignment", a genuine
		// assignment silently becomes a command word.
		{`a[0]=1`, false, "plain subscript assignment"},
		{`a[i]=x`, false, "name subscript assignment"},
		{`a[i]+=x`, false, "append assignment"},
		{`a[b[c]]=1`, false, "nested subscript assignment"},
		{`a[$i]=x`, false, "expanded subscript assignment"},
		{`a[i + 1]=x`, false, "arithmetic subscript assignment"},

		// Shapes that already behaved correctly, kept as controls.
		{`f[int]`, false, "bare name subscript, no assignment"},
		{`f[*]`, false, "glob star"},
		{`f[-x]`, false, "leading dash"},
		{`f[]`, false, "empty subscript"},
		{`echo f[[]x]`, false, "argument position was never affected"},
		{`ls a[[]b]`, false, "argument position was never affected"},
	}

	variants := []struct {
		name string
		opts []ParserOption
	}{
		{"bash", []ParserOption{Variant(LangBash)}},
		{"bash-posix", []ParserOption{Variant(LangBash), PosixMode(true)}},
		{"posix", []ParserOption{Variant(LangPOSIX)}},
	}
	for _, variant := range variants {
		for _, tc := range tests {
			t.Run(variant.name+"/"+tc.src, func(t *testing.T) {
				_, err := NewParser(variant.opts...).Parse(strings.NewReader(tc.src), "")
				if tc.wantErr && err == nil {
					t.Fatalf("%s: expected a parse error (%s)", tc.src, tc.why)
				}
				if !tc.wantErr && err != nil {
					t.Fatalf("%s: real bash 5.3 accepts this (%s), got: %v", tc.src, tc.why, err)
				}
			})
		}
	}
}

func TestNameBracketStreamingAndWordSemantics(t *testing.T) {
	for _, src := range []string{
		`f[[]int]`,
		`f[map[string]int]`,
		`f[*T]`,
		`f[[]]`,
		`f[[x]]`,
		`f[$x]`,
		`f[$(printf x)]`,
	} {
		t.Run(src, func(t *testing.T) {
			f, err := NewParser().Parse(iotest.OneByteReader(strings.NewReader(src)), "")
			if err != nil {
				t.Fatal(err)
			}
			var got bytes.Buffer
			if err := NewPrinter().Print(&got, f); err != nil {
				t.Fatal(err)
			}
			if got.String() != src+"\n" {
				t.Fatalf("ordinary glob word changed meaning: got %q, want %q", got.String(), src+"\n")
			}
		})
	}
}

// TestNameBracketInvalidAssignmentDefersToRuntime pins the other half of the
// boundary. A subscript that is not valid arithmetic is NOT a parse error, even
// with an `=` following: bash 5.3 scans to the matching bracket and complains
// only when it EVALUATES the subscript, so each of these is a runtime
// "arithmetic syntax error" with status 1 and the rest of the script still
// runs. (Verified against GNU bash 5.3.15; interp reproduces the diagnostics,
// and tests/arith.tests line 358 covers `a[b[c]d]=e`.)
func TestNameBracketInvalidAssignmentDefersToRuntime(t *testing.T) {
	for _, src := range []string{
		`a[b[c]d]=e`,
		`a[[]int]=x`,
		`a[map[string]int]=x`,
		`a[*T]=x`,
		`a[[]]=x`,
		`a[[x]]=x`,
	} {
		t.Run(src, func(t *testing.T) {
			f, err := NewParser().Parse(iotest.OneByteReader(strings.NewReader(src)), "")
			if err != nil {
				t.Fatalf("%s: bash accepts this at parse time: %v", src, err)
			}
			call, ok := f.Stmts[0].Cmd.(*CallExpr)
			if !ok || len(call.Assigns) != 1 {
				t.Fatalf("%s: want one assignment, got %#v", src, f.Stmts[0].Cmd)
			}
			if call.Assigns[0].Index == nil {
				t.Fatalf("%s: assignment lost its subscript", src)
			}
		})
	}
}

// TestNameBracketAssignmentStillParsesAsAssignment guards the half of the fix a
// verdict-only test cannot see: an assignment must still be an ASSIGNMENT, not
// merely something that parses. Accepting `a[0]=1` as a command word would pass
// the table above while silently changing the program's meaning.
func TestNameBracketAssignmentStillParsesAsAssignment(t *testing.T) {
	for _, src := range []string{`a[0]=1`, `a[i]=x`, `a[i]+=x`, `a[b[c]]=1`} {
		f, err := NewParser().Parse(iotest.OneByteReader(strings.NewReader(src)), "")
		if err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		call, ok := f.Stmts[0].Cmd.(*CallExpr)
		if !ok {
			t.Fatalf("%s: want *CallExpr, got %T", src, f.Stmts[0].Cmd)
		}
		if len(call.Assigns) != 1 {
			t.Fatalf("%s: want 1 assignment, got %d — the subscript lookahead "+
				"turned a real assignment into a command word", src, len(call.Assigns))
		}
		if call.Assigns[0].Name == nil || call.Assigns[0].Index == nil {
			t.Fatalf("%s: assignment lost its name or subscript: %#v", src, call.Assigns[0])
		}
	}
}
