// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package syntax

import (
	"strings"
	"testing"
)

// The Bash++ start-site decision table, tested against the measured corpus.
//
// A NOTE ON WHAT "CLASS" MEANS HERE, because two different questions wear the
// same word and conflating them silently produces a wrong table.
//
// The corpus in bashpp-tests/tools/startsites answers: "what does stock bash
// 5.3 do with this COMPLETE string?" The parser needs a different answer:
// "what does stock bash 5.3 do at this COMMIT POINT?" For a single-line shape
// the two coincide. For a multi-line one they do not, and they diverge in the
// direction that matters:
//
//	if err != nil { echo a }    complete → REJECT → Class R
//	if err != nil {             commit point (+ then…fi) → ACCEPT → Class E
//
// Both measurements are correct and they describe different strings. The
// parser commits at the opening line, so the COMMIT-POINT class is the one it
// must honour — and it is the riskier of the two, because Class E is what
// requires an escape and a fallback. Reading the complete-form class as the
// commit-point class would understate the compatibility risk on exactly the
// constructs where care is most needed.
//
// So the rows below are commit-point rows, and the corpus's prefix-* ids are
// the ones they correspond to.

type startSiteCase struct {
	corpusID string // the id in bashpp-tests/tools/startsites/shapes.tsv
	src      string
	want     StartSite
	class    SiteClass
}

// Day-1 (P1) sites the recognizer claims. Every class here is the COMMIT-POINT
// class, cross-checked against the corpus in TestStartSiteCorpusAgreement.
var day1Cases = []startSiteCase{
	// var / const / type — all Class E: each is an ordinary command word in
	// bash today, so each needs a table row and a `command …` escape.
	{"decl-var-assign", "var x = 1", StartVar, ClassE},
	{"decl-var-typed", "var x int = 1", StartVar, ClassE},
	{"decl-var-bare", "var x int", StartVar, ClassE},
	{"decl-const", "const K = 2", StartConst, ClassE},
	{"decl-const-typed", "const K int = 2", StartConst, ClassE},
	{"decl-type-bare", "type T int", StartTypeDecl, ClassE},
	{"decl-type-alias", "type ID = string", StartTypeDecl, ClassE},
	{"prefix-type-struct", "type T struct {", StartTypeDecl, ClassE},
	{"sharp-enum", "type Color enum { Red; Green }", StartTypeDecl, ClassE},

	// := splits across BOTH classes. This is the single most useful thing the
	// measured corpus produced, and it is invisible to inspection.
	{"short-scalar-int", "x := 42", StartShortDecl, ClassE},
	{"short-scalar-string", `x := "hello"`, StartShortDecl, ClassE},
	{"short-composite-map", `m := map[string]int{"a": 1}`, StartShortDecl, ClassE},
	{"short-composite-slice", "s := []int{1, 2, 3}", StartShortDecl, ClassE},
	{"short-composite-struct", `g := Gopher{Name: "x"}`, StartShortDecl, ClassE},
	{"short-index-generic", "f := Max[int]", StartShortDecl, ClassE},
	{"short-multi-literal", "x, y := 1, 2", StartShortDecl, ClassE},
	{"short-multi-three", "x, y, z := 1, 2, 3", StartShortDecl, ClassE},
	{"short-call", "x := f()", StartShortDecl, ClassR},
	{"short-call-args", "x := f(1, 2)", StartShortDecl, ClassR},
	{"short-multi-call", "x, y := f()", StartShortDecl, ClassR},
	{"short-multi-err", `config, err := readConfig("config.json")`, StartShortDecl, ClassR},

	// Bare calls — always Class R. The parenthesis is the free disambiguator.
	{"call-bare", "f(1, 2)", StartGoCall, ClassR},
	{"call-selector", "x.y.z()", StartGoCall, ClassR},
	{"call-builtin-clear", "clear(m)", StartGoCall, ClassR},
}

func TestStartSiteDay1(t *testing.T) {
	t.Parallel()
	for _, tc := range day1Cases {
		t.Run(tc.corpusID, func(t *testing.T) {
			got := RecognizeStartSite(tc.src)
			if got.Site != tc.want {
				t.Fatalf("RecognizeStartSite(%q) site = %v, want %v", tc.src, got.Site, tc.want)
			}
			if got.Class != tc.class {
				t.Fatalf("RecognizeStartSite(%q) class = %v, want %v", tc.src, got.Class, tc.class)
			}
			if !got.Bounded {
				t.Fatalf("RecognizeStartSite(%q) was not decided within %d bytes; "+
					"the streaming parser cannot supply unbounded lookahead",
					tc.src, maxLookahead)
			}
		})
	}
}

func TestStartSiteSourceFence(t *testing.T) {
	for _, src := range []string{"~~~python", "~~~~python", "~~~python as py", "~~~go", "~~~go as native",
		"~~~tf as iac", "~~~tf as iac !my-tofu", "~~~tf as !tofu", "~~~dockerfile !img-runner", "~~~foo as x !run_it",
		"~~~my-config as cfg !my-runner", "~~~c++ as cxx",
		// Sprint 473 #2395: the language rides past maxLookahead on a wide
		// opener, so the fence must be read off the whole opener word.
		strings.Repeat("~", 64) + "python", strings.Repeat("~", 200) + "python"} {
		got := RecognizeStartSite(src)
		if got.Site != StartSource || got.Class != ClassE || !got.Bounded {
			t.Fatalf("RecognizeStartSite(%q) = %#v", src, got)
		}
	}
	for _, src := range []string{" ~~~python", "~~python", "~~~", "~~~python nope", "```python",
		"~~~tf as", "~~~tf as iac extra", "~~~tf !", "~~~tf !-bad", "~~~tf !a/b", "~~~tf as !my-tofu", "~~~tf as !x !y", "~~~tf !x as y",
		"~~~-bad as x", "~~~a/b as x", "~~~a.b as x", strings.Repeat("~", 64), strings.Repeat("~", 200)} {
		if got := RecognizeStartSite(src); got.Site != StartNone {
			t.Fatalf("RecognizeStartSite(%q) = %#v, want no match", src, got)
		}
	}
}

// Case 2 of the five-case matrix: NEAR MISS. The shape with its signal removed
// must fall back to shell. These are the inputs most likely to be broken by an
// over-eager recognizer, because each one differs from a claimed shape by a
// single token.
func TestStartSiteNearMiss(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, src string }{
		{"bare var command", "var"},
		{"var with no identifier", "var = 1"},
		{"variable is not var", "variable=1"},
		{"typeset is not type", "typeset -i x"},
		{"const as a program name", "const"},
		{"single colon is not :=", "x : 42"},
		{"assignment is not short decl", "x=42"},
		{"spaced equals is not short decl", "x = 42"},
		{"function definition, not a call", "f()"},
		{"spaced function definition", "f ()"},
		{"command with args, no parens", "f 1 2"},
		{"trailing := with no rhs", "x :="},
		{"comma with no :=", "x, y"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RecognizeStartSite(tc.src); got.Site != StartNone {
				t.Fatalf("RecognizeStartSite(%q) = %v, want StartNone (must stay shell)",
					tc.src, got.Site)
			}
		})
	}
}

// TWO GATES, NOT ONE — this test found the distinction by failing.
//
// Any construct sharing a start site with a
// supported form cannot be rejected by the recognizer at all, so its fallback
// must be enforced where the body is parsed. A reviewer checking only this
// test would conclude the fallback is covered when it is not.
//
// Case 5: UNSUPPORTED FORM. A Go shape belonging to a phase P1 has not reached
// must return StartNone, so that a Class E shape keeps running as the shell
// command it is today. Emitting a Bash++ diagnostic here would break working
// scripts, which is the one failure mode the whole design forbids.
func TestStartSiteLaterPhasesStayShell(t *testing.T) {
	t.Parallel()
	later := []string{
		`defer cleanup`,       // P3-A: the plain command form stays shell (Class E)
		`go worker(a, b)`,     // P4
		`ch <- v`,             // P4
		`select {`,            // P4
		`switch x {`,          // later
		`for i := range 10 {`, // later
		`if err != nil {`,     // later — needs a completing context
		`match x {`,           // rejected candidate
	}
	for _, src := range later {
		t.Run(src, func(t *testing.T) {
			if got := RecognizeStartSite(src); got.Site != StartNone {
				t.Fatalf("RecognizeStartSite(%q) = %v, want StartNone: "+
					"an unimplemented phase must fall back to shell, never claim the shape",
					src, got.Site)
			}
		})
	}
}

// TestStartSiteFuncDefer covers the P3-A start sites. Both are Class R: a
// `func name(` and a `defer f(` are bash syntax errors, so claiming them takes
// nothing away, while the bare `defer cleanup` and the anonymous `func()`
// function-definition shapes must stay shell.
func TestStartSiteFuncDefer(t *testing.T) {
	t.Parallel()
	claims := []struct {
		src  string
		want StartSite
	}{
		{`func f(x int) int {`, StartFunc},
		{`func f() {`, StartFunc},
		{`func run(a, b) {`, StartFunc},
		{`defer f(x)`, StartDefer},
		{`defer f()`, StartDefer},
		{`defer pkg.Close(h)`, StartDefer},
	}
	for _, tc := range claims {
		t.Run(tc.src, func(t *testing.T) {
			got := RecognizeStartSite(tc.src)
			if got.Site != tc.want || got.Class != ClassR || !got.Bounded {
				t.Fatalf("RecognizeStartSite(%q) = %#v, want %v/R/bounded", tc.src, got, tc.want)
			}
		})
	}
	stayShell := []string{
		`func`,          // bare keyword
		`func()`,        // anonymous function definition, Class E
		`func () {`,     // spaced anonymous definition
		`func if(x) {`,  // a Go keyword cannot be a func name
		`defer`,         // bare command
		`defer cleanup`, // plain command form, Class E
		// The Go-form defer call is additive; a shell command named `defer`
		// remains covered by the plain-command near miss above.
	}
	for _, src := range stayShell {
		t.Run("shell/"+src, func(t *testing.T) {
			if got := RecognizeStartSite(src); got.Site != StartNone {
				t.Fatalf("RecognizeStartSite(%q) = %v, want StartNone", src, got.Site)
			}
		})
	}
}

func TestStartSiteImport(t *testing.T) {
	for _, src := range []string{`import "fmt"`, `import f "fmt"`} {
		if got := RecognizeStartSite(src); got.Site != StartImport || got.Class != ClassE || !got.Bounded {
			t.Fatalf("RecognizeStartSite(%q) = %#v", src, got)
		}
	}
}

// The escapes published in the design of record must actually work. They work
// by making the position not a Bash++ command position at all, so the
// recognizer must never fire on them.
func TestStartSiteEscapes(t *testing.T) {
	t.Parallel()
	for _, src := range []string{
		"command var x=1",
		`"var" x=1`,
		"command const K=2",
		"command type T struct {",
		`'var' x=1`,
	} {
		t.Run(src, func(t *testing.T) {
			if got := RecognizeStartSite(src); got.Site != StartNone {
				t.Fatalf("escape %q was claimed as %v; the published escape must win",
					src, got.Site)
			}
		})
	}
}

// The bounded-lookahead property, asserted rather than assumed.
//
// sh's parser is streaming and non-backtracking. An earlier fix attempt failed
// three times by scanning past the end of a buffered chunk, where the
// conservative answer silently restored the old behaviour — a failure that
// looks exactly like success. Every Day-1 recognizer must therefore decide
// within maxLookahead bytes, and padding the input must not change the verdict.
func TestStartSiteBoundedLookahead(t *testing.T) {
	t.Parallel()
	for _, tc := range day1Cases {
		t.Run(tc.corpusID, func(t *testing.T) {
			want := RecognizeStartSite(tc.src)
			padded := tc.src + strings.Repeat(" # trailing", 40)
			if got := RecognizeStartSite(padded); got.Site != want.Site || got.Class != want.Class {
				t.Fatalf("verdict changed when input was padded past %d bytes: %v/%v -> %v/%v; "+
					"the recognizer is reading further than the parser can guarantee",
					maxLookahead, want.Site, want.Class, got.Site, got.Class)
			}
			// Truncating to the budget must also preserve the verdict, which
			// is what proves the decision never needed the tail.
			head := tc.src
			if len(head) > maxLookahead {
				head = head[:maxLookahead]
			}
			if got := RecognizeStartSite(head); got.Site != want.Site {
				t.Fatalf("verdict needed more than %d bytes: %v -> %v",
					maxLookahead, want.Site, got.Site)
			}
		})
	}
}

// A recognizer that claims a shape must never claim the empty or blank input,
// which is the cheapest way to catch a fallthrough bug in the dispatch chain.
func TestStartSiteEmptyInput(t *testing.T) {
	t.Parallel()
	for _, src := range []string{"", " ", "\t", "\n", "   \t  "} {
		if got := RecognizeStartSite(src); got.Site != StartNone {
			t.Fatalf("RecognizeStartSite(%q) = %v, want StartNone", src, got.Site)
		}
	}
}
