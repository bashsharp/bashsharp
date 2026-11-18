// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package syntax

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"testing/iotest"
)

// Reserved declaration near misses now diagnose in Bash++ while explicit
// shell escapes retain AST identity. Both reader chunking and POSIX composition
// remain covered; classic grammar checks are unchanged.

// bashppReadModes feeds the same source to the parser two ways: in one piece,
// and one byte at a time.
var bashppReadModes = []struct {
	name string
	wrap func(string) io.Reader
}{
	{"whole", func(s string) io.Reader { return strings.NewReader(s) }},
	{"one-byte", func(s string) io.Reader { return iotest.OneByteReader(strings.NewReader(s)) }},
}

// bashppParseAs parses in under lang in one specific configuration. Comments
// are kept so that a divergence in comment placement cannot hide.
func bashppParseAs(lang LangVariant, in string, posix bool, wrap func(string) io.Reader) (*File, error) {
	p := NewParser(Variant(lang), KeepComments(true), PosixMode(posix))
	return p.Parse(wrap(in), "")
}

// bashppCheckIdentical asserts that LangBashPP is indistinguishable from
// LangBash for in, in every configuration of bashppReadModes x PosixMode.
func bashppCheckIdentical(t *testing.T, in string) {
	t.Helper()
	for _, posix := range []bool{false, true} {
		for _, mode := range bashppReadModes {
			name := mode.name
			if posix {
				name += "/posix"
			}
			t.Run(name, func(t *testing.T) {
				bashFile, bashErr := bashppParseAs(LangBash, in, posix, mode.wrap)
				ppFile, ppErr := bashppParseAs(LangBashPP, in, posix, mode.wrap)
				switch {
				case (bashErr == nil) != (ppErr == nil):
					t.Fatalf("input %q: bash err=%s but bashpp err=%s",
						in, errText(bashErr), errText(ppErr))
				case bashErr != nil:
					if diff := bashppErrDiff(bashErr, ppErr); diff != "" {
						t.Fatalf("input %q: %s", in, diff)
					}
				default:
					if diff := bashppTreeDiff(bashFile, ppFile); diff != "" {
						t.Fatalf("input %q must stay shell, but bashpp parsed it "+
							"differently: %s", in, diff)
					}
				}
			})
		}
	}
}

// bashppRejectionEvidence is the evidence that sent the first tranche of this
// story back, kept as its own list so it cannot be lost in the larger one.
//
// Every entry is an ORDINARY BASH COMMAND that the first cut of the dispatch
// claimed as a *BashPPDecl. The first three name something Go reserves, so
// they are not declarations in any phase — `if`, `type` and `return` are
// keywords, not identifiers, and no amount of later implementation makes them
// declarable. The last two have the supported ARITY and a last word that is
// not a Go expression at all, which is exactly what an arity check cannot see.
//
// They are exercised twice: [TestBashPPRejectedShapesClaimNothing] asserts the
// narrow claim (nothing was claimed), and they are folded into
// bashppUnsupportedDeclBodies below so the full identity assertion — AST,
// positions, printed bytes, both readers, both POSIX settings — covers the
// remaining shell forms. Reserved forms now have explicit diagnostic policy.
// They also reach the compatibility gate through bashppSharedCorpus.
var bashppRejectionEvidence = []string{
	"var if = 1",
	"var type = 1",
	"const return = 1",
	"var x = 1,",
	"var x = {1}",
}

// bashppGoKeywords is every Go keyword, spelled out here rather than read from
// the production set, so that the test and the implementation are two
// independent statements of the same list. A keyword dropped from
// goReservedWords fails TestBashPPReservedNamesStayShell instead of silently
// becoming declarable.
var bashppGoKeywords = []string{
	"break", "case", "chan", "const", "continue", "default", "defer", "else",
	"fallthrough", "for", "func", "go", "goto", "if", "import", "interface",
	"map", "package", "range", "return", "select", "struct", "switch", "type",
	"var",
}

// bashppUnsupportedDeclBodies are the shapes that open like a Day-1 var/const
// declaration and are not one. Sprint198 makes reserved forms diagnostics;
// escaped and unrelated shell forms still parse identically to LangBash.
var bashppUnsupportedDeclBodies = []struct{ name, in string }{
	// The two named witnesses: a supported body with a word glued on the end.
	// A prefix-committing dispatch claims both.
	{"trailing word after value", "var x = 1 extra"},
	{"two words after =", "var x = foo bar"},
	{"const with trailing word", "const K = 2 extra"},
	{"many trailing words", "var x = 1 a b c d e"},

	// Arity below the supported form.
	{"bare keyword", "var"},
	{"keyword and name only", "var x"},
	{"no value", "var x ="},
	{"const bare", "const"},

	// The separator is not `=`.
	{"short decl operator", "var x := 1"},
	{"no separator", "var x 1"},
	{"glued assignment", "var x=1"},
	{"keyword then bare =", "var = 1"},

	// The name is not a Go identifier, or is not a bare word at all.
	{"hyphenated name", "var x-y = 1"},
	{"expanded name", "var $x = 1"},
	{"quoted name", `var "x" = 1`},
	{"flag-shaped name", "var -x = 1"},

	// REJECTION EVIDENCE, GROUP ONE: a Go reserved word where the name goes.
	// Go has no identifier `if`, so `var if = 1` is not an unsupported
	// declaration — it is not a declaration. Bash runs all three today as
	// three-argument commands, so claiming them breaks working scripts at a
	// site no phase can ever legitimately claim. The full keyword sweep is
	// TestBashPPReservedNamesStayShell; these are the reported witnesses.
	{"reserved name if", "var if = 1"},
	{"reserved name type", "var type = 1"},
	{"reserved name return", "const return = 1"},
	{"reserved name as short decl target", "if := 1"},

	// REJECTION EVIDENCE, GROUP TWO: the right arity, and a last word that is
	// not a Go expression. This is the group an arity check cannot see, and
	// the reason bashppInitKind spells the initializer grammar out.
	{"trailing comma value", "var x = 1,"},
	{"braced value", "var x = {1}"},

	// The rest of the initializer grammar's boundary, each falling back in
	// silence. See bashppInitKind for why every one of these is deferred
	// rather than merely unimplemented: a shape with no published Class E row
	// has no licence to diverge.
	{"string value", `var x = "a b"`},
	{"single-quoted value", "var x = 'q'"},
	{"float value", "var x = 1.5"},
	{"hex value", "var x = 0x1f"},
	{"leading-zero value", "var x = 007"},
	{"underscore-separated value", "var x = 1_000"},
	{"negative value", "var x = -1"},
	{"flag-shaped value", "var x = -f"},
	{"identifier value", "var x = y"},
	{"expanded value", "var x = $y"},
	{"braced expansion value", "var x = ${y}"},
	{"command substitution value", "var x = $(id)"},
	{"backquoted value", "var x = `id`"},
	{"glob value", "var x = *"},
	{"path value", "var x = a/b"},
	{"assignment-shaped value", "var x = a=b"},
	{"empty string value", `var x = ""`},

	// The published escapes. These work by making the position not a Bash++
	// command position at all, so they must be identical whatever the body.
	{"command escape", "command var x = 1"},
	{"quoted keyword escape", `"var" x = 1`},
	{"single-quoted keyword escape", `'var' x = 1`},

	// Redirects mean the shell is doing something the declaration shape has
	// nowhere to put, so the whole command stays shell.
	{"trailing redirect", "var x = 1 > out"},
	{"leading redirect", "> out var x = 1"},

	// A leading assignment is a shell prefix assignment, not a declaration.
	{"assignment prefix", "e=1 var x = 1"},

	// Multi-line inputs, where the unsupported body is not the first command
	// and the parser has already been through a newline.
	{"unsupported on second line", "echo hi\nvar x = 1 extra"},
	{"two unsupported lines", "var x = 1 extra\nvar y = 2 extra"},

	// Nested command positions: the dispatch fires at every command position,
	// so the fallback has to hold at every one of them too.
	{"inside command substitution", "echo $(var x = 1 extra)"},
	{"inside a function body", "f() { var x = 1 extra; }"},
	{"inside an if", "if true; then var x = foo bar; fi"},
}

func bashppCheckDiagnostic(t *testing.T, in, want string) {
	t.Helper()
	for _, posix := range []bool{false, true} {
		for _, mode := range bashppReadModes {
			_, err := bashppParseAs(LangBashPP, in, posix, mode.wrap)
			pe, ok := err.(ParseError)
			if !ok || pe.Text != want || pe.Pos.Line() == 0 || pe.Pos.Col() == 0 {
				t.Errorf("%q %s posix=%v: got %v; want positioned %q", in, mode.name, posix, err, want)
			}
		}
	}
}

func TestBashPPUnsupportedDeclBodyContract(t *testing.T) {
	t.Parallel()
	for _, tc := range bashppUnsupportedDeclBodies {
		t.Run(tc.name, func(t *testing.T) {
			if want := bashppSprint198Diagnostic(tc.in); want != "" {
				bashppCheckDiagnostic(t, tc.in, want)
				return
			}
			if tc.in == "if := 1" {
				bashppCheckDiagnostic(t, tc.in, "`if <cond>` must be followed by `then`")
				return
			}
			bashppCheckIdentical(t, tc.in)
		})
	}
}

// THE ACCEPTED SIDE. Everything above proves the dispatch keeps its hands off
// what it does not understand; what follows proves it does something exact
// with what it does.
//
// The claim under test is not "a BashPPDecl appears". It is that the node
// carries the WHOLE classification — which start site opened it, which bytes
// each part came from — because the design of record makes this the only place
// the decision is stored. No later phase re-reads the source to work out
// whether a region is Go or shell; the node's existence is the decision, and
// its positions are the only record of where the decision applies. A node with
// a right shape and wrong positions would print correctly today and misplace
// every diagnostic and every edit an embedder makes tomorrow.

// bashppAcceptedDecl is one claimed declaration, spelled out down to the byte
// offsets its parts must occupy.
type bashppAcceptedDecl struct {
	in   string
	site StartSite
	kw   string
	name string
	init string

	// Byte offsets, half-open, of the whole declaration and of each part.
	// Written out rather than derived from in, so that a change in what the
	// dispatch consumes has to be restated here deliberately.
	declStart, declEnd uint
	kwStart, kwEnd     uint
	nameStart, nameEnd uint
	initStart, initEnd uint
}

var bashppAcceptedDecls = []bashppAcceptedDecl{
	{
		in: "var x = 1", site: StartVar, kw: "var", name: "x", init: "1",
		declStart: 0, declEnd: 9,
		kwStart: 0, kwEnd: 3,
		nameStart: 4, nameEnd: 5,
		initStart: 8, initEnd: 9,
	},
	{
		in: "const K = 2", site: StartConst, kw: "const", name: "K", init: "2",
		declStart: 0, declEnd: 11,
		kwStart: 0, kwEnd: 5,
		nameStart: 6, nameEnd: 7,
		initStart: 10, initEnd: 11,
	},
	{
		// Irregular spacing: the declaration ENDS at the value, not at the
		// trailing blank, and each part still points at its own bytes. This is
		// the case that catches a node built from token counts instead of from
		// the words the parser produced.
		in: "var   longName   =   12345  ", site: StartVar, kw: "var", name: "longName", init: "12345",
		declStart: 0, declEnd: 26,
		kwStart: 0, kwEnd: 3,
		nameStart: 6, nameEnd: 14,
		initStart: 21, initEnd: 26,
	},
	{
		// Not at the start of the input, so the offsets cannot come out right
		// by accident.
		in: "echo hi\nvar x = 1", site: StartVar, kw: "var", name: "x", init: "1",
		declStart: 8, declEnd: 17,
		kwStart: 8, kwEnd: 11,
		nameStart: 12, nameEnd: 13,
		initStart: 16, initEnd: 17,
	},
	{
		// A multi-digit value, to pin that it is the initializer's KIND and not
		// its bytes that the grammar and the divergence licence turn on. `1`
		// is the published row's value; `42` is the same shape, licensed by
		// the same row, and must be claimed identically.
		in: "const Retries = 42", site: StartConst, kw: "const", name: "Retries", init: "42",
		declStart: 0, declEnd: 18,
		kwStart: 0, kwEnd: 5,
		nameStart: 6, nameEnd: 13,
		initStart: 16, initEnd: 18,
	},
	{
		// Zero, the one decimal literal that may begin with `0`.
		in: "var x = 0", site: StartVar, kw: "var", name: "x", init: "0",
		declStart: 0, declEnd: 9,
		kwStart: 0, kwEnd: 3,
		nameStart: 4, nameEnd: 5,
		initStart: 8, initEnd: 9,
	},
}

// bashppLastDecl finds the single *BashPPDecl in f, failing if there is not
// exactly one.
func bashppLastDecl(t *testing.T, f *File) *BashPPDecl {
	t.Helper()
	var found []*BashPPDecl
	Walk(f, func(n Node) bool {
		if d, ok := n.(*BashPPDecl); ok {
			found = append(found, d)
		}
		return true
	})
	if len(found) != 1 {
		t.Fatalf("found %d BashPPDecl nodes, want exactly 1", len(found))
	}
	return found[0]
}

// TestBashPPUntypedDeclClassified is the positive counterpart to
// TestBashPPUnsupportedDeclBodyStaysShell: the supported body is claimed, in
// every reader and POSIX configuration, and the node records where it came
// from.
func TestBashPPUntypedDeclClassified(t *testing.T) {
	t.Parallel()

	for _, tc := range bashppAcceptedDecls {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			for _, posix := range []bool{false, true} {
				for _, mode := range bashppReadModes {
					name := mode.name
					if posix {
						name += "/posix"
					}
					t.Run(name, func(t *testing.T) {
						f, err := bashppParseAs(LangBashPP, tc.in, posix, mode.wrap)
						if err != nil {
							t.Fatalf("LangBashPP rejected %q: %v", tc.in, err)
						}
						decl := bashppLastDecl(t, f)

						if decl.Site != tc.site {
							t.Errorf("site = %v, want %v", decl.Site, tc.site)
						}
						if decl.Kw.Value != tc.kw {
							t.Errorf("keyword = %q, want %q", decl.Kw.Value, tc.kw)
						}
						if decl.Name.Value != tc.name {
							t.Errorf("name = %q, want %q", decl.Name.Value, tc.name)
						}
						// The UNTYPED form is the whole of this story, so a
						// claimed node may never carry a type. The typed body
						// falls back to shell instead; see
						// bashppUnsupportedDeclBodies.
						if decl.DeclType != nil {
							t.Errorf("type = %v, want nil: only the untyped form is claimed", decl.DeclType)
						}
						if len(decl.Init) != 1 {
							t.Fatalf("initializer has %d words, want 1", len(decl.Init))
						}
						if got := decl.Init[0].Lit(); got != tc.init {
							t.Errorf("initializer literal = %q, want %q", got, tc.init)
						}

						checkSpan := func(what string, n Node, start, end uint) {
							t.Helper()
							if got, want := n.Pos().Offset(), start; got != want {
								t.Errorf("%s starts at offset %d, want %d", what, got, want)
							}
							if got, want := n.End().Offset(), end; got != want {
								t.Errorf("%s ends at offset %d, want %d", what, got, want)
							}
						}
						checkSpan("declaration", decl, tc.declStart, tc.declEnd)
						checkSpan("keyword", decl.Kw, tc.kwStart, tc.kwEnd)
						checkSpan("name", decl.Name, tc.nameStart, tc.nameEnd)
						checkSpan("initializer", decl.Init[0], tc.initStart, tc.initEnd)
					})
				}
			}
		})
	}
}

// TestBashPPDeclWalk pins the visitor traversal.
//
// Walk is not a formality for a new Command: its default arm panics on a node
// type it does not know, so an unlisted node turns every Walk-based consumer —
// shfmt -s, every embedder's own analysis, and the compatibility gate's own
// position comparison in bashppNodeOffsets — into a crash. The order and the
// spans are asserted together because a traversal that visits the right nodes
// in the wrong order is just as wrong for a printer driven by positions.
func TestBashPPDeclWalk(t *testing.T) {
	t.Parallel()

	f, err := bashppParse(LangBashPP, "var x = 1")
	if err != nil {
		t.Fatal(err)
	}

	var visited []string
	Walk(f, func(n Node) bool {
		if n == nil {
			return true
		}
		visited = append(visited, fmt.Sprintf("%T[%d,%d)",
			n, n.Pos().Offset(), n.End().Offset()))
		return true
	})

	want := []string{
		"*syntax.File[0,9)",
		"*syntax.Stmt[0,9)",
		"*syntax.BashPPDecl[0,9)",
		"*syntax.Lit[0,3)",  // var
		"*syntax.Lit[4,5)",  // x
		"*syntax.Word[8,9)", // the initializer
		"*syntax.Lit[8,9)",
	}
	if !slices.Equal(visited, want) {
		t.Fatalf("Walk visited\n  %v\nwant\n  %v", visited, want)
	}

	// The `=` at offset 6 is deliberately absent from the tree. BashPPDecl has
	// no field for it — the separator is part of the shape, not part of the
	// declaration's content — so the printer writes it back from the shape
	// rather than from a recorded position. Asserted so that adding a field
	// for it later is a decision rather than an accident.
	for _, v := range visited {
		if strings.Contains(v, "[6,7)") {
			t.Errorf("Walk visited a node at the `=` (offset 6): %s", v)
		}
	}
}

// TestBashPPDeclPrint covers the printer arm, which is the other half of
// "include Walk/Printer positions".
//
// Idempotence is the real assertion. A claimed declaration must print to
// something LangBashPP re-parses into the same declaration, or the tree stops
// round-tripping through shfmt and the classification is lost on the first
// format. Printing the same bytes as LangBash is not required — this is a
// licensed divergence — but it is what these shapes happen to do, and that is
// worth pinning: it means formatting a script does not rewrite the lines
// Bash++ claims.
func TestBashPPDeclPrint(t *testing.T) {
	t.Parallel()

	cases := []struct{ in, want string }{
		{"var x = 1", "var x = 1\n"},
		{"const K = 2", "const K = 2\n"},
		{"var   longName   =   12345  ", "var longName = 12345\n"},
		{"const Retries = 42", "const Retries = 42\n"},
		{"var x = 1; echo done", "var x = 1\necho done\n"},
		{"var x = 1 | cat", "var x = 1 | cat\n"},
		{"if true; then var x = 1; fi", "if true; then var x = 1; fi\n"},
		{"echo $(var x = 1)", "echo $(var x = 1)\n"},
		{"f() { const K = 2; }", "f() { const K = 2; }\n"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			f, err := bashppParse(LangBashPP, tc.in)
			if err != nil {
				t.Fatalf("LangBashPP rejected %q: %v", tc.in, err)
			}
			got, err := bashppPrint(f)
			if err != nil {
				t.Fatalf("printing %q: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("printed %q, want %q", got, tc.want)
			}

			// Round-trip: the printed form must re-parse to the same tree,
			// and printing it again must be a fixed point.
			f2, err := bashppParse(LangBashPP, got)
			if err != nil {
				t.Fatalf("LangBashPP rejected its own output %q: %v", got, err)
			}
			if _, ok := bashppFirstDecl(f2); !ok {
				t.Fatalf("re-parsing %q lost the declaration", got)
			}
			got2, err := bashppPrint(f2)
			if err != nil {
				t.Fatalf("re-printing %q: %v", got, err)
			}
			if got2 != got {
				t.Fatalf("printing is not idempotent: %q then %q", got, got2)
			}
		})
	}
}

func bashppFirstDecl(f *File) (*BashPPDecl, bool) {
	var found *BashPPDecl
	Walk(f, func(n Node) bool {
		if found != nil {
			return false
		}
		if d, ok := n.(*BashPPDecl); ok {
			found = d
		}
		return true
	})
	return found, found != nil
}

// TestBashPPDeclOnlyInBashPP is the containment check. Every other variant —
// including the other bash-like ones, which share almost all of the grammar —
// must be untouched by the dispatch, or this stops being an opt-in dialect.
func TestBashPPDeclOnlyInBashPP(t *testing.T) {
	t.Parallel()

	for _, lang := range []LangVariant{LangBash, LangPOSIX, LangMirBSDKorn, LangBats, LangZsh} {
		t.Run(lang.String(), func(t *testing.T) {
			f, err := bashppParse(lang, "var x = 1")
			if err != nil {
				t.Fatalf("%v rejected %q: %v", lang, "var x = 1", err)
			}
			if d, ok := bashppFirstDecl(f); ok {
				t.Fatalf("%v produced a %T; the dispatch must be LangBashPP-only", lang, d)
			}
		})
	}
}

// Go-reserved declared names remain invalid; the reserved declaration
// introducer now diagnoses instead of executing a shell command.
func TestBashPPReservedNamesDiagnose(t *testing.T) {
	t.Parallel()
	for _, kw := range bashppGoKeywords {
		for _, decl := range []string{"var", "const"} {
			in := decl + " " + kw + " = 1"
			t.Run(in, func(t *testing.T) {
				if got := RecognizeStartSite(in); got.Site != StartNone {
					t.Errorf("invalid declared name recognized: %v", got.Site)
				}
				bashppCheckDiagnostic(t, in, "invalid "+decl+" statement; use command "+decl+" to invoke a shell command")
			})
		}
	}
}

// TestBashPPInitGrammarIsClosed pins the Day-1 initializer grammar directly,
// below the parser, so a change to it is a deliberate edit to a table rather
// than a side effect noticed later in a compatibility gate.
//
// The accepted column is the whole of what this story implements: a Go decimal
// integer literal, and nothing else. The rejected column is not a list of
// mistakes — every entry is a form Bash++ will plausibly support one day, and
// each is refused TODAY because the licence to diverge is granted per measured
// shape and no row names it yet. See bashppInitKind.
func TestBashPPInitGrammarIsClosed(t *testing.T) {
	t.Parallel()

	accepted := []string{"0", "1", "9", "42", "1234567890"}
	rejected := []string{
		"", "007", "0x1f", "0b1", "0o7", "1_000", "-1", "+1", "1.5", "1e3",
		"1,", "{1}", "a", "true", "false", "nil", "x1", "1x", "1a", "*", "-f",
	}

	for _, s := range accepted {
		if !bashppIsDecimalInt(s) {
			t.Errorf("bashppIsDecimalInt(%q) = false, want true", s)
		}
	}
	for _, s := range rejected {
		if bashppIsDecimalInt(s) {
			t.Errorf("bashppIsDecimalInt(%q) = true, want false", s)
		}
	}

	// And the same answers through the word-level entry point the dispatch
	// actually calls, which additionally refuses anything that is not a bare
	// literal — a quote or an expansion means the shell is doing something the
	// Go grammar has no reading of.
	for _, tc := range []struct{ in, want string }{
		{"var x = 1", "INT"},
		{"var x = 0", "INT"},
		{"var x = 42", "INT"},
		{`var x = "1"`, ""},
		{"var x = $y", ""},
		{"var x = 1,", ""},
		{"var x = {1}", ""},
	} {
		f, err := bashppParse(LangBash, tc.in)
		if err != nil {
			t.Fatalf("LangBash rejected %q: %v", tc.in, err)
		}
		call, ok := f.Stmts[0].Cmd.(*CallExpr)
		if !ok || len(call.Args) != 4 {
			t.Fatalf("%q did not parse as a four-word command", tc.in)
		}
		if got := bashppInitKind(call.Args[3]); got != tc.want {
			t.Errorf("bashppInitKind of the value in %q = %q, want %q", tc.in, got, tc.want)
		}
	}
}
