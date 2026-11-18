// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package syntax

import (
	"testing"

	"github.com/go-quicktest/qt"
)

// TestBashPPRangeLinesCall pins the one chained call a range admits:
// `x.Lines()`. Anything else after the operand's `(` keeps rewinding.
func TestBashPPRangeLinesCall(t *testing.T) {
	t.Run("run and start spellings round-trip", func(t *testing.T) {
		f := bashppRoundTrip(t, bashppWrapFunc("for line := range r.Lines() {\n\t\techo \"$line\"\n\t}"))
		rng := f.Stmts[0].Cmd.(*BashPPFuncDecl).Body.Stmts[0].Cmd.(*BashPPRange)
		qt.Assert(t, qt.IsNotNil(rng.Call))
		qt.Assert(t, qt.HasLen(rng.Call.Fun, 2))
		qt.Assert(t, qt.Equals(rng.Call.Fun[0].Value, "r"))
		qt.Assert(t, qt.Equals(rng.Call.Fun[1].Value, "Lines"))
		qt.Assert(t, qt.IsNil(rng.Expr))
		qt.Assert(t, qt.Equals(rng.Chan.Lit(), "r.Lines()"))
		qt.Assert(t, qt.Equals(rng.Call.Rparen.Col(), uint(28)))
	})
	t.Run("walk reaches the call", func(t *testing.T) {
		f := bashppRoundTrip(t, bashppWrapFunc("for range p.Lines() {\n\t\techo hi\n\t}"))
		calls := 0
		Walk(f, func(n Node) bool {
			if c, ok := n.(*BashPPCall); ok && len(c.Fun) == 2 && c.Fun[1].Value == "Lines" {
				calls++
			}
			return true
		})
		qt.Assert(t, qt.Equals(calls, 1))
	})
	t.Run("other chained shapes are not claimed", func(t *testing.T) {
		for _, src := range []string{
			"for line := range r.Lines(1) {\n\t\techo hi\n\t}",
			"for line := range r.Other() {\n\t\techo hi\n\t}",
			"for line := range a.b.Lines() {\n\t\techo hi\n\t}",
		} {
			_, err := bashppParse(LangBashPP, bashppWrapFunc(src))
			qt.Assert(t, qt.IsNotNil(err), qt.Commentf("src: %s", src))
		}
	})
}
