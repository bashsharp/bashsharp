// Copyright (c) 2026, the bash++ authors
// See LICENSE for licensing information

package interp_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/go-quicktest/qt"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// classicModeProof is byte-for-byte the probe the bash 5.3 compliance suite
// runs before any fixture (bashy tools/bash53suite proveBashPPMode): the
// shell marker must always print, and the extension marker prints only when
// the Bash++ grammar is live, because `type Bash53GateProof int` is a type
// declaration in the dialect and a failing `type` builtin call in Classic.
const classicModeProof = "printf '%s\\n' bash53-shell-active\nset -e\ntype Bash53GateProof int\nprintf '%s\\n' bash53-bashpp-active\n"

// TestClassicActivationHiddenOptionKeepsGrammar is the sh half of the
// compliance suite's Bash++ mode proof. The classic harness selects the
// dialect with interp.Lang and hides it from option enumeration with
// HideBashPPOption; hiding must not cost the grammar. Sprint 243 measured a
// refusal of this exact probe on a certification host and traced it to an
// inherited POSIXLY_CORRECT=1 (startup POSIX mode), not to this seam.
func TestClassicActivationHiddenOptionKeepsGrammar(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP), interp.HideBashPPOption())
	qt.Assert(t, qt.Equals(r.LangVariant(), syntax.LangBashPP))
	qt.Assert(t, qt.Equals(r.Dialect(), syntax.LangBashPP))
	qt.Assert(t, qt.IsFalse(r.PosixMode()))

	parser := syntax.NewParser(syntax.Variant(r.LangVariant()), syntax.PosixMode(r.PosixMode()))
	prog, err := parser.Parse(strings.NewReader(classicModeProof), "mode-proof.sh")
	qt.Assert(t, qt.IsNil(err))
	// The probe's third statement is a typed declaration, not a `type` call.
	_, isDecl := prog.Stmts[2].Cmd.(*syntax.BashPPDecl)
	qt.Assert(t, qt.IsTrue(isDecl), qt.Commentf("stmt 3 parsed as %T", prog.Stmts[2].Cmd))

	qt.Assert(t, qt.IsNil(r.Run(context.Background(), prog)), qt.Commentf("output:\n%s", out.String()))
	qt.Assert(t, qt.Equals(out.String(), "bash53-shell-active\nbash53-bashpp-active\n"))

	// Hidden means hidden: the live dialect stays out of the option listing.
	out.Reset()
	bashPPRun(t, r, "set +o")
	qt.Assert(t, qt.IsFalse(strings.Contains(out.String(), "bashpp")), qt.Commentf("set +o:\n%s", out.String()))
}

// TestClassicActivationOffRefusesGrammar is the OFF contract: a Classic
// runner parses the probe as a `type` builtin call, which fails on the
// unknown names and, under `set -e`, stops the script with status 1 before
// the extension marker.
func TestClassicActivationOffRefusesGrammar(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		opts []interp.RunnerOption
	}{
		{"default", nil},
		{"explicit bash", []interp.RunnerOption{interp.Lang(syntax.LangBash)}},
		{"hidden option without dialect", []interp.RunnerOption{interp.HideBashPPOption()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			r := bashPPRunner(t, &out, tc.opts...)
			qt.Assert(t, qt.Equals(r.LangVariant(), syntax.LangBash))
			parser := syntax.NewParser(syntax.Variant(r.LangVariant()))
			prog, err := parser.Parse(strings.NewReader(classicModeProof), "mode-proof.sh")
			qt.Assert(t, qt.IsNil(err))
			_, isCall := prog.Stmts[2].Cmd.(*syntax.CallExpr)
			qt.Assert(t, qt.IsTrue(isCall), qt.Commentf("stmt 3 parsed as %T", prog.Stmts[2].Cmd))

			err = r.Run(context.Background(), prog)
			var status interp.ExitStatus
			qt.Assert(t, qt.IsTrue(errors.As(err, &status)), qt.Commentf("err=%v output:\n%s", err, out.String()))
			qt.Assert(t, qt.Equals(status, interp.ExitStatus(1)))
			qt.Assert(t, qt.IsTrue(strings.HasPrefix(out.String(), "bash53-shell-active\n")))
			qt.Assert(t, qt.IsTrue(strings.Contains(out.String(), "Bash53GateProof: not found")))
			qt.Assert(t, qt.IsFalse(strings.Contains(out.String(), "bash53-bashpp-active")))
		})
	}
}

// TestClassicActivationPosixInert is the POSIX contract that explains the
// Sprint 243 certification-host refusal: with startup POSIX mode on (bash
// enters it for an inherited POSIXLY_CORRECT, `--posix`, or `-o posix`), the
// runner reports LangPOSIX from LangVariant even when the Bash++ dialect was
// selected and hidden, so the front door's parser never sees the grammar and
// the probe is refused exactly as in Classic OFF.
func TestClassicActivationPosixInert(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP), interp.HideBashPPOption(), interp.WithPosixMode(true))
	qt.Assert(t, qt.IsTrue(r.PosixMode()))
	qt.Assert(t, qt.Equals(r.Dialect(), syntax.LangBashPP))
	qt.Assert(t, qt.Equals(r.LangVariant(), syntax.LangPOSIX))

	// The front door translates LangPOSIX to Bash grammar + POSIX parse
	// rules; either way the typed declaration is not recognized.
	for _, variant := range []syntax.LangVariant{syntax.LangPOSIX, syntax.LangBash} {
		parser := syntax.NewParser(syntax.Variant(variant), syntax.PosixMode(true))
		prog, err := parser.Parse(strings.NewReader(classicModeProof), "mode-proof.sh")
		qt.Assert(t, qt.IsNil(err))
		_, isCall := prog.Stmts[2].Cmd.(*syntax.CallExpr)
		qt.Assert(t, qt.IsTrue(isCall), qt.Commentf("%v: stmt 3 parsed as %T", variant, prog.Stmts[2].Cmd))
	}

	// An inherited POSIXLY_CORRECT reaches the same state at runtime.
	out.Reset()
	r2 := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP), interp.HideBashPPOption())
	qt.Assert(t, qt.Equals(r2.LangVariant(), syntax.LangBashPP))
	bashPPRun(t, r2, "POSIXLY_CORRECT=1")
	qt.Assert(t, qt.IsTrue(r2.PosixMode()))
	qt.Assert(t, qt.Equals(r2.LangVariant(), syntax.LangPOSIX))
	// `set +o posix` hands the dialect back without reconstruction. (Bash also
	// leaves POSIX mode on `unset POSIXLY_CORRECT`; this runner's unset hook
	// does not yet mirror that, which is outside the activation seam.)
	bashPPRun(t, r2, "set +o posix")
	qt.Assert(t, qt.IsFalse(r2.PosixMode()))
	qt.Assert(t, qt.Equals(r2.LangVariant(), syntax.LangBashPP))
}
