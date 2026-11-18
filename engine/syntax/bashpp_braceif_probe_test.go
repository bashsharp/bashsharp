// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package syntax

import (
	"strings"
	"testing"
)

// TestBraceIfProbe probes the exact parser properties that make a streaming-safe
// Go brace-form `if` fundamentally different from every other Day-1 site, and
// records the conclusions as executable claims.
//
// BACKGROUND. Every other Day-1 site decides within 64 bytes of the command
// position. `if` cannot, because stock bash 5.3 ACCEPTS the opening brace as
// an ordinary word in the condition:
//
//	if { true; } then echo yes; fi     # legal bash — `{` is a command word
//	if test -d /tmp { echo yes }       # legal bash — `{` is an argument word
//
// The Go brace-form `if err != nil { echo oops }` is therefore ambiguous with
// legal shell AT THE COMMIT POINT: the brace might be an argument, and the
// parser cannot know which until it finds either `then` (shell) or `}` without
// a preceding `then` (Go). That scan is unbounded, and the parser is
// streaming.
//
// These probes prove why StartGoIf cannot join the bounded recognizer. The
// implementation uses a complete parser transaction instead, scoped to an
// already committed Bash++ function body; see bashpp_if.go.

// Probe 1: `{` is a legal word in bash `if` conditions.
//
// This is the root cause. A bounded recognizer at `if` must decide whether `{`
// opens a Go body or is a condition word. Both are legal in stock bash, so a
// bounded decision at the brace is wrong for one of them.
func TestBraceIfProbe_BraceIsLegalInCondition(t *testing.T) {
	t.Parallel()

	// Each of these is a legal bash script where `{` appears in the condition
	// of an `if` statement and is NOT a Go body opener.
	legit := []struct {
		name string
		src  string
	}{
		// `{` as a compound command opener in the condition.
		{"brace group in condition", "if { true; } then echo yes; fi"},
		// `{` as a literal argument word (brace expansion candidate).
		{"brace as argument word", "if test -f {a,b}.txt; then echo yes; fi"},
		// `{` at the end of the condition line, followed by `then` on the
		// next line — the exact position where a bounded recognizer would
		// mistakenly commit.
		{"brace at end of condition", "if test -d /tmp {\nthen echo yes; fi"},
	}
	for _, tc := range legit {
		t.Run(tc.name, func(t *testing.T) {
			p := NewParser(Variant(LangBash), KeepComments(true))
			f, err := p.Parse(strings.NewReader(tc.src), "")
			if err != nil {
				t.Fatalf("bash rejects %q: %v — this is a legal bash script "+
					"and the parser must accept it", tc.src, err)
			}
			// Confirm the tree contains an IfClause with a `then`.
			found := false
			Walk(f, func(n Node) bool {
				if _, ok := n.(*IfClause); ok {
					found = true
				}
				return true
			})
			if !found {
				t.Fatalf("parsed %q but found no IfClause; the AST is wrong", tc.src)
			}
		})
	}
}

// Probe 2: the commit point needs the MATCHING brace, not just ANY brace.
//
// The condition itself may contain nested braces (brace groups, brace
// expansions), so the recognizer would need to track brace depth. This makes
// the scan length a function of the input, not a constant, which is the
// definition of "unbounded" in this parser's terms.
func TestBraceIfProbe_NestedBracesInCondition(t *testing.T) {
	t.Parallel()

	// A condition with nested brace groups. The `{` at the end of the
	// condition matches its own `}`, not the outer `fi`.
	src := "if { { true; }; true; } then echo yes; fi"
	p := NewParser(Variant(LangBash), KeepComments(true))
	f, err := p.Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatalf("bash rejects %q: %v", src, err)
	}
	found := false
	Walk(f, func(n Node) bool {
		if _, ok := n.(*IfClause); ok {
			found = true
		}
		return true
	})
	if !found {
		t.Fatalf("parsed %q but found no IfClause", src)
	}
}

// Probe 3: the bounded-lookahead property CANNOT hold for `if`.
//
// Every Day-1 recognizer decides within maxLookahead (64) bytes. This probe
// constructs a legal bash `if` whose condition word `{` sits at byte 0 of the
// condition (byte 3 of the full input), and whose `then` sits more than 64
// bytes past the `if`. A bounded recognizer MUST misclassify this input.
func TestBraceIfProbe_UnboundedCommitPoint(t *testing.T) {
	t.Parallel()

	// Build: `if CMD [long args...] { \n then echo yes; fi`
	// The condition is padded with harmless argument words so that `{` sits
	// beyond byte 64 from the `if`, and `then` sits even further.
	// "if " is 3 bytes; we need {+then to be past byte 64 from byte 0.
	cond := "if true"
	for len(cond) < maxLookahead+10 {
		cond += " arg"
	}
	src := cond + " {\nthen echo yes; fi"

	// The bounded window from byte 0 must not contain `{` or `then`.
	prefix := src
	if len(prefix) > maxLookahead {
		prefix = prefix[:maxLookahead]
	}
	if strings.Contains(prefix, "{") {
		t.Fatalf("test is miscalibrated: '{' is inside the bounded window")
	}
	if strings.Contains(prefix, "then") {
		t.Fatalf("test is miscalibrated: 'then' is inside the bounded window")
	}

	// But the full input IS legal bash.
	p := NewParser(Variant(LangBash), KeepComments(true))
	_, err := p.Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatalf("bash rejects %q: %v — the recognizer would have to reject "+
			"a legal bash script to decide within %d bytes", src, err, maxLookahead)
	}
}

// Probe 4: `for` gets braces easily because its commit point IS bounded.
//
// In `for x in a b; { ... }`, the `{` appears where the parser expects `do`.
// The loop expression has already been fully consumed, so `{` vs `do` is a
// 1-token lookahead. This is why `for` has a `Braces` field and `if` cannot
// simply copy the trick.
func TestBraceIfProbe_ForClauseHasBoundedBraces(t *testing.T) {
	t.Parallel()

	src := "for x in a b; { echo $x; }"
	p := NewParser(Variant(LangBash), KeepComments(true))
	f, err := p.Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatalf("bash rejects %q: %v", src, err)
	}
	found := false
	Walk(f, func(n Node) bool {
		if fc, ok := n.(*ForClause); ok && fc.Braces {
			found = true
		}
		return true
	})
	if !found {
		t.Fatalf("parsed %q but found no braced ForClause; expected Braces=true", src)
	}
}

// Probe 5: RecognizeStartSite correctly returns noMatch for `if`.
//
// This is the existing contract, reasserted here as part of the probe suite:
// because the commit point is unbounded, the recognizer MUST NOT claim the
// shape, and the 64-byte bound is preserved by exclusion.
func TestBraceIfProbe_RecognizerReturnsNoMatch(t *testing.T) {
	t.Parallel()

	shapes := []string{
		"if err != nil {",
		"if x > 0 {",
		"if test -f /tmp/x {",
		"if true {",
	}
	for _, src := range shapes {
		t.Run(src, func(t *testing.T) {
			got := RecognizeStartSite(src)
			if got.Site != StartNone {
				t.Fatalf("RecognizeStartSite(%q) = %v, want StartNone: "+
					"the commit point is unbounded and must stay unrecognized",
					src, got.Site)
			}
		})
	}
}

// Probe 6: the BashPPIf node implements Command and has correct position
// semantics independently of parsing.
func TestBraceIfProbe_NodeIsReady(t *testing.T) {
	t.Parallel()

	node := &BashPPIf{
		If: newTestPos(1, 1, 0),
		Cond: &BashPPIdent{Name: &Lit{
			ValuePos: newTestPos(1, 4, 3), ValueEnd: newTestPos(1, 8, 7), Value: "true",
		}},
		Then: &Block{
			Lbrace: newTestPos(1, 15, 14),
			Rbrace: newTestPos(1, 30, 29),
		},
	}

	// Verify the Command interface.
	var _ Command = node
	node.commandNode()

	if node.Pos() != node.If {
		t.Fatalf("Pos() = %v, want %v", node.Pos(), node.If)
	}
	if node.End() != node.Then.End() {
		t.Fatalf("End() = %v, want %v (Then.End)", node.End(), node.Then.End())
	}

	// With an else branch, End() follows the else.
	elseBlock := &Block{
		Lbrace: newTestPos(1, 40, 39),
		Rbrace: newTestPos(1, 55, 54),
	}
	node.Else = elseBlock
	if node.End() != elseBlock.End() {
		t.Fatalf("End() with Else = %v, want %v", node.End(), elseBlock.End())
	}
}

// newTestPos is a helper for building test positions.
func newTestPos(line, col, offset uint) Pos {
	return NewPos(offset, line, col)
}
