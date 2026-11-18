// Copyright (c) 2026, the bash++ authors
// See LICENSE for licensing information

package syntax

import (
	"slices"
	"strings"
	"testing"

	"github.com/go-quicktest/qt"
	"github.com/google/go-cmp/cmp"
)

// TestBashPPVariant covers the variant's plumbing: it is accepted by the
// parser, and it round-trips through the flag.Value interface.
func TestBashPPVariant(t *testing.T) {
	t.Parallel()

	qt.Assert(t, qt.Equals(LangBashPP.String(), "bashpp"))

	for _, s := range []string{"bashpp", "bash++"} {
		var l LangVariant
		qt.Assert(t, qt.IsNil(l.Set(s)))
		qt.Assert(t, qt.Equals(l, LangBashPP))
	}

	// Adding the variant must not have renumbered the existing bits, as
	// LangVariant values are part of the public API.
	qt.Assert(t, qt.Equals(LangBash, 1<<0))
	qt.Assert(t, qt.Equals(LangPOSIX, 1<<1))
	qt.Assert(t, qt.Equals(LangMirBSDKorn, 1<<2))
	qt.Assert(t, qt.Equals(LangBats, 1<<3))
	qt.Assert(t, qt.Equals(LangZsh, 1<<4))
	qt.Assert(t, qt.Equals(LangAuto, 1<<5))
	qt.Assert(t, qt.Equals(LangBashPP, 1<<6))

	// bash++ is bash-like, and bash-exact: it gets every Bash parse rule,
	// including those which the other bash-like variants do not share.
	qt.Assert(t, qt.IsTrue(LangBashPP.in(langBashLike)))
	qt.Assert(t, qt.IsTrue(LangBashPP.in(langBashExact)))
	qt.Assert(t, qt.IsFalse(LangBats.in(langBashExact)))
}

// bashppParse parses in under lang, with comments kept so that a divergence in
// comment placement cannot hide.
func bashppParse(lang LangVariant, in string) (*File, error) {
	p := NewParser(Variant(lang), KeepComments(true))
	return p.Parse(strings.NewReader(in), "")
}

func bashppPrint(f *File) (string, error) {
	var sb strings.Builder
	err := NewPrinter().Print(&sb, f)
	return sb.String(), err
}

// bashppNodeOffsets flattens every node's start and end offset. cmpOpt ignores
// positions, so identical ASTs still have to be shown to sit at identical
// offsets — a Bash++ region that consumed a different amount of source would
// otherwise compare equal.
func bashppNodeOffsets(f *File) []uint {
	var out []uint
	Walk(f, func(n Node) bool {
		if n != nil {
			out = append(out, n.Pos().Offset(), n.End().Offset())
		}
		return true
	})
	return out
}

// bashppTreeDiff describes how two successful parses differ, or returns "" when
// they are identical in AST, node positions and printed bytes.
func bashppTreeDiff(bashFile, ppFile *File) string {
	if diff := cmp.Diff(bashFile, ppFile, cmpOpt); diff != "" {
		return "AST differs (-bash +bashpp):\n" + diff
	}
	if !slices.Equal(bashppNodeOffsets(bashFile), bashppNodeOffsets(ppFile)) {
		return "node positions differ"
	}
	bashOut, bashErr := bashppPrint(bashFile)
	ppOut, ppErr := bashppPrint(ppFile)
	if (bashErr == nil) != (ppErr == nil) {
		return "printing differs: bash err=" + errText(bashErr) + " bashpp err=" + errText(ppErr)
	}
	if bashOut != ppOut {
		return "printed bytes differ: bash " + bashppQuoted(bashOut) + " bashpp " + bashppQuoted(ppOut)
	}
	return ""
}

// bashppErrDiff compares two parse errors, ignoring the language name the
// message embeds, and returns "" when they say the same thing.
func bashppErrDiff(bashErr, ppErr error) string {
	bashMsg := strings.ReplaceAll(bashErr.Error(), "bashpp", "")
	bashMsg = strings.ReplaceAll(bashMsg, "bash", "")
	ppMsg := strings.ReplaceAll(ppErr.Error(), "bashpp", "")
	ppMsg = strings.ReplaceAll(ppMsg, "bash", "")
	if bashMsg == ppMsg {
		return ""
	}
	return "bash err " + bashppQuoted(bashErr.Error()) + " != bashpp err " + bashppQuoted(ppErr.Error())
}

// bashppParseDiff is the whole-input comparison in one call: "" means LangBash
// and LangBashPP agree completely, including on whether the input is an error.
func bashppParseDiff(t *testing.T, in string) string {
	t.Helper()
	bashFile, bashErr := bashppParse(LangBash, in)
	ppFile, ppErr := bashppParse(LangBashPP, in)
	switch {
	case (bashErr == nil) != (ppErr == nil):
		return "bash err=" + errText(bashErr) + " but bashpp err=" + errText(ppErr)
	case bashErr != nil:
		return bashppErrDiff(bashErr, ppErr)
	default:
		return bashppTreeDiff(bashFile, ppFile)
	}
}

func errText(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}

func bashppQuoted(s string) string { return "\"" + s + "\"" }

// bashppAcceptedShapes is the Bash++ half of the shared corpus: the shapes the
// parser actually claims today.
//
// Until the command-position dispatch landed, the shared corpus was the bash
// parser's own corpus and nothing else, so every input compared identical and
// the licensing branch of TestBashPPMatchesBash was never reached. A gate that
// only ever exercises its "no divergence" path cannot tell a working allowlist
// from a broken one — the same shape of vacuous success the bounded-lookahead
// work hit three times — so the claimed shapes are corpus inputs like any
// other, and the gate now requires the licensed count to be nonzero.
//
// Every entry must be attributable to a published Class E row by exact shape.
// That is not a formality: it is the reason each line here is a shape the
// corpus in bashpp-tests/tools/startsites measured against stock bash, rather
// than a shape that merely looks like one. The longer entries exist to put a
// claimed shape at command positions other than the start of the input — after
// a keyword, inside a substitution, inside a function body — because the
// dispatch fires at all of them and attribution has to survive all of them.
var bashppAcceptedShapes = []string{
	// The two published untyped declarations, bare.
	"var x = 1",
	"const K = 2",

	// The same shapes with a different name and a different value. These are
	// the entries that make the gate check a LICENCE rather than a string: the
	// published rows spell `x`/`1` and `K`/`2`, and a licence bound to bytes
	// would report both of these as unlisted divergences. See bashppDeclShape.
	"var counter = 42",
	"const Retries = 0",

	// The same shapes ending at each terminator bashppShapeBoundary accepts,
	// which is what a real script looks like.
	"var x = 1; echo done",
	"var x = 1 | cat",
	"var x = 1 # note",
	"const K = 2\nvar x = 1",

	// Command positions that are not the start of the input.
	"if true; then var x = 1; fi",
	"echo $(var x = 1)",
	"f() { const K = 2; }",
	"while false; do const K = 2; done",
}

// bashppSharedCorpus is every input the parser tests know about: the valid
// corpus, the error corpus, the printer's own cases, and the Bash++ shapes the
// parser claims.
func bashppSharedCorpus(t *testing.T) []string {
	t.Helper()
	var inputs []string
	for _, cs := range [][]fileTestCase{fileTests, fileTestsNoPrint, fileTestsKeepComments} {
		for _, c := range cs {
			inputs = append(inputs, c.inputs...)
		}
	}
	for _, c := range errorCases {
		inputs = append(inputs, c.in)
	}
	for _, c := range printTests {
		inputs = append(inputs, c.in)
	}
	inputs = append(inputs, bashppAcceptedShapes...)
	// The near-misses belong in the gate too, and not only in their own test.
	// They are the inputs a widened grammar or a loosened name check would
	// start claiming, and the gate is where such a claim shows up as an
	// UNLISTED DIVERGENCE with the shape named — which is a far more useful
	// failure than a bare inequality. The rejection evidence reaches the
	// corpus this way as well, since bashppUnsupportedDeclBodies carries it.
	for _, tc := range bashppUnsupportedDeclBodies {
		inputs = append(inputs, tc.in)
	}
	inputs = append(inputs, bashppRejectionEvidence...)
	for in := range bashppSprint198DiagnosticRows {
		inputs = append(inputs, in)
	}
	if len(inputs) < 500 {
		t.Fatalf("corpus looks too small to be meaningful: %d inputs", len(inputs))
	}
	return inputs
}

// TestBashPPMatchesBash is the compatibility gate, and it is the executable
// form of the Bash++ compatibility contract rather than a blanket identity
// claim.
//
// It used to assert that LangBashPP produced byte-identical ASTs to LangBash
// for every input in the shared corpus. That was true only while the
// command-position dispatch was absent; the dispatch breaks it BY DESIGN, at
// the published Class E sites and nowhere else. Deleting or weakening the test
// at that point would discard the only gate that can tell a designed
// divergence from a regression, so the claim is re-expressed instead:
//
//  1. Rejection changes require an exact Sprint198 policy row and diagnostic.
//     Every other classic input must remain accepted.
//  2. MEANING PRESERVED, OR NAMED — for an input LangBash parses, the ASTs,
//     node positions and printed bytes must be identical unless a published
//     Class E row licenses a divergence at a real bash command position. The
//     licensing rows live in bashpp_divergence_test.go and each names a corpus
//     id; the row must name the diverging SHAPE, not merely share its start
//     site. AN UNLISTED DIVERGENCE IS A FAILURE.
//  3. ADDITIVE ONLY — for an input LangBash rejects, LangBashPP may accept it;
//     that is Class R ground and no working script occupies it. But the
//     acceptance must still be attributable to a recognized start site, so a
//     parser bug cannot hide behind "purely additive". If both reject, the
//     diagnostics must still agree.
//
// While the dispatch is absent every input takes the identical path, and the
// counts logged at the end say so. The licensing machinery is exercised
// independently by TestBashPPLicenseRejectsUnlisted so that it cannot pass
// vacuously in the meantime.
func TestBashPPMatchesBash(t *testing.T) {
	t.Parallel()

	inputs := bashppSharedCorpus(t)

	var identical, licensed, additive, rejected, policyRejected int
	for _, in := range inputs {
		bashFile, bashErr := bashppParse(LangBash, in)
		ppFile, ppErr := bashppParse(LangBashPP, in)

		if bashppSprint198Diagnostic(in) != "" {
			if bashErr != nil || !bashppSprint198DiagnosticMatches(in, ppErr) {
				t.Errorf("policy row %q: classic=%v, Bash++=%v; want %q", in, bashErr, ppErr, bashppSprint198Diagnostic(in))
			}
			policyRejected++
			continue
		}
		if bashErr != nil {
			// Rule 3 — bash rejects, so this is purely additive ground.
			if ppErr != nil {
				if diff := bashppErrDiff(bashErr, ppErr); diff != "" {
					t.Errorf("input %q: %s", in, diff)
				}
				rejected++
				continue
			}
			hits := bashppLineSites(in)
			if len(hits) == 0 {
				t.Errorf("UNATTRIBUTABLE ACCEPTANCE for input %q:\n"+
					"  LangBash rejects it (%v) but LangBashPP accepts it, and no\n"+
					"  Bash++ start site was recognized anywhere in it. Additive\n"+
					"  behaviour must come from a start site; this is a parser bug\n"+
					"  or a recognizer the start-site table does not describe.",
					in, bashErr)
				continue
			}
			additive++
			continue
		}

		// Rule 1 — bash accepts, so bash++ must accept.
		if ppErr != nil {
			t.Errorf("REGRESSION for input %q:\n"+
				"  LangBash parses it but LangBashPP rejects it (%v).\n"+
				"  No explicit Sprint198 policy row licenses this rejection.", in, ppErr)
			continue
		}

		hits := bashppCommandSites(bashFile, ppFile, in)

		// A Class R verdict at a command position of an input bash just
		// ACCEPTED is a contradiction: Class R means bash rejects the shape.
		// The recognizer and the measured corpus disagree, and the table is
		// what is wrong, not the input.
		for _, h := range hits {
			if h.match.Class == ClassR {
				t.Errorf("TABLE CONTRADICTION for input %q:\n"+
					"  LangBash parses this input, yet the start-site table calls\n"+
					"  %s Class R — i.e. claims bash rejects it.\n"+
					"  Re-measure the corpus or fix the recognizer.", in, h)
			}
		}

		diff := bashppTreeDiff(bashFile, ppFile)
		if diff == "" {
			identical++
			continue
		}

		// Rule 2 — a divergence needs a published Class E row.
		named, unlicensed := bashppLicense(hits)
		if len(named) == 0 || len(unlicensed) > 0 {
			t.Errorf("UNLISTED DIVERGENCE for input %q:\n"+
				"  %s\n"+
				"  Recognized sites: %v (unlicensed: %v)\n"+
				"  LangBashPP may only parse differently from LangBash where a\n"+
				"  published Class E row names THIS SHAPE — sharing a start site\n"+
				"  with a published row is not a license. Either this divergence\n"+
				"  is a regression, or the shape belongs in\n"+
				"  bashppAllowedDivergences with a corpus id that\n"+
				"  verify-day1-table.sh can trace to baseline.tsv.",
				in, diff, hits, unlicensed)
			continue
		}
		licensed++
		t.Logf("licensed divergence for input %q at published row(s) %v", in, bashppRowIDs(named))
	}

	t.Logf("bash/bashpp corpus: %d inputs — %d identical, %d licensed divergences, "+
		"%d additive acceptances, %d rejected by both, %d policy diagnostics",
		len(inputs), identical, licensed, additive, rejected, policyRejected)

	// The gate must never be able to report success without having compared
	// anything. This is the cheap guard against a corpus that silently stops
	// being collected.
	if identical == 0 {
		t.Errorf("not one input compared identical across %d inputs; the gate is "+
			"almost certainly not running against the corpus it thinks it is",
			len(inputs))
	}

	// And it must never report success without having exercised the licensing
	// branch. While the dispatch was absent every input took the identical
	// path, so a broken allowlist and a working one produced the same report;
	// now that LangBashPP claims shapes, at least one input must reach rule 2
	// and be named by a published row. Zero here means either the dispatch
	// stopped claiming anything or bashppAcceptedShapes stopped reaching the
	// corpus, and both look exactly like a pass from the outside.
	if licensed == 0 {
		t.Errorf("not one licensed divergence across %d inputs, though LangBashPP "+
			"claims %d shapes; rule 2 of the compatibility contract was never "+
			"exercised, so this run cannot distinguish a working allowlist from "+
			"a broken one", len(inputs), len(bashppAcceptedShapes))
	}
}

// TestBashPPQuoteMatchesBash covers the other LangVariant-driven code path in
// this package: Quote's per-language escaping must treat bash++ as bash.
func TestBashPPQuoteMatchesBash(t *testing.T) {
	t.Parallel()

	for _, s := range []string{
		"", "foo", "foo bar", "a'b", `a"b`, "a\\b", "$foo", "a\tb", "a\nb",
		"\x00\x01", "\xff", "héllo", "é", "a`b", "a#b", "a{b,c}",
	} {
		bashOut, bashErr := Quote(s, LangBash)
		ppOut, ppErr := Quote(s, LangBashPP)
		qt.Check(t, qt.Equals(ppOut, bashOut), qt.Commentf("Quote(%q)", s))
		qt.Check(t, qt.Equals(ppErr == nil, bashErr == nil), qt.Commentf("Quote(%q) err", s))
	}
}
