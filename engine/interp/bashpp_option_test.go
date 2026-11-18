// Copyright (c) 2026, the bash++ authors
// See LICENSE for licensing information

package interp_test

import (
	"strings"
	"testing"

	"github.com/go-quicktest/qt"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// TestBashPPSetOptionLiveToggle proves `set -o bashpp` / `set +o bashpp` is a
// live dialect toggle: a plain-bash runner enters and leaves the bash++ dialect
// at runtime, and object-valued variables follow the toggle. This is the state
// half of the runtime seam — no parser dispatch is involved, since the grammar
// is identical (the same LangBashPP parse feeds every case here).
func TestBashPPSetOptionLiveToggle(t *testing.T) {
	t.Parallel()

	var out strings.Builder
	r := bashPPRunner(t, &out)

	// A runner that never mentions a dialect is plain bash; objects are off.
	qt.Assert(t, qt.Equals(r.Dialect(), syntax.LangBash))
	qt.Assert(t, qt.ErrorIs(r.SetObject("OBJ", 1), interp.ErrObjectsUnsupported))

	// `set -o bashpp` enters the dialect live: the effective dialect flips and
	// objects become usable, all without reconstructing the runner.
	bashPPRun(t, r, `set -o bashpp`)
	qt.Assert(t, qt.Equals(r.Dialect(), syntax.LangBashPP))
	qt.Assert(t, qt.IsNil(r.SetObject("OBJ", map[string]any{"a": 1})))
	got, ok := r.Object("OBJ")
	qt.Assert(t, qt.IsTrue(ok))
	gotMap, ok := got.(map[string]any)
	qt.Assert(t, qt.IsTrue(ok))
	qt.Assert(t, qt.DeepEquals(gotMap, map[string]any{"a": 1}))

	// `set +o bashpp` leaves the dialect live again: objects go away.
	bashPPRun(t, r, `set +o bashpp`)
	qt.Assert(t, qt.Equals(r.Dialect(), syntax.LangBash))
	qt.Assert(t, qt.ErrorIs(r.SetObject("OBJ2", 2), interp.ErrObjectsUnsupported))
}

// TestBashPPSetOptionDisableFromDialect proves the toggle works the other way:
// a runner constructed in the bash++ dialect can be dropped to plain bash at
// runtime with `set +o bashpp`, and brought back with `set -o bashpp`.
func TestBashPPSetOptionDisableFromDialect(t *testing.T) {
	t.Parallel()

	var out strings.Builder
	r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
	qt.Assert(t, qt.Equals(r.Dialect(), syntax.LangBashPP))

	bashPPRun(t, r, `set +o bashpp`)
	qt.Assert(t, qt.Equals(r.Dialect(), syntax.LangBash))
	qt.Assert(t, qt.ErrorIs(r.SetObject("OBJ", 1), interp.ErrObjectsUnsupported))

	bashPPRun(t, r, `set -o bashpp`)
	qt.Assert(t, qt.Equals(r.Dialect(), syntax.LangBashPP))
	qt.Assert(t, qt.IsNil(r.SetObject("OBJ", 1)))
}

// TestBashPPSetOptionConstruction proves the option flows through the shared
// option parser at construction too (`Params("-o", "bashpp")`), before any
// expand.Config exists — the same code path `set -o bashpp` takes at runtime.
func TestBashPPSetOptionConstruction(t *testing.T) {
	t.Parallel()

	r, err := interp.New(interp.Params("-o", "bashpp"))
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(r.Dialect(), syntax.LangBashPP))
	qt.Assert(t, qt.IsNil(r.SetObject("OBJ", 1)))
}

// TestBashPPSetOptionListing proves the reporting half: `set -o` and `set +o`
// surface bashpp's live state in both bash's human and reusable forms.
func TestBashPPSetOptionListing(t *testing.T) {
	t.Parallel()

	line := func(opts ...interp.RunnerOption) func(script string) string {
		return func(script string) string {
			var out strings.Builder
			r := bashPPRunner(t, &out, opts...)
			bashPPRun(t, r, script)
			for l := range strings.SplitSeq(out.String(), "\n") {
				if strings.Contains(l, "bashpp") {
					return l
				}
			}
			return ""
		}
	}

	// Human form (`set -o`): a padded `name<tab>on/off` line. A plain-bash
	// runner reports it off; after `set -o bashpp` it reports on.
	qt.Assert(t, qt.Equals(line()("set -o"), ""))
	qt.Assert(t, qt.Equals(line()("set -o bashpp; set -o"), "bashpp         \ton"))

	// Reusable form (`set +o`): a `set {-o,+o} bashpp` line, off then on.
	qt.Assert(t, qt.Equals(line()("set +o"), ""))
	qt.Assert(t, qt.Equals(line()("set -o bashpp; set +o"), "set -o bashpp"))

	// A runner constructed in the dialect reports it on out of the box.
	qt.Assert(t, qt.Equals(
		line(interp.Lang(syntax.LangBashPP))("set +o"), "set -o bashpp"))
	qt.Assert(t, qt.Equals(
		line(interp.Lang(syntax.LangBashPP), interp.HideBashPPOption())("set +o"), ""))

	// bashpp is sorted into the alphabetical listing, right before braceexpand.
	full := func(script string) string {
		var out strings.Builder
		r := bashPPRunner(t, &out)
		bashPPRun(t, r, script)
		return out.String()
	}
	got := full("set -o bashpp; set +o")
	bashppIdx := strings.Index(got, "set -o bashpp")
	braceIdx := strings.Index(got, "braceexpand")
	qt.Assert(t, qt.IsTrue(bashppIdx >= 0 && braceIdx >= 0))
	qt.Assert(t, qt.IsTrue(bashppIdx < braceIdx))
}

func TestBashPPShoptPOSIXOptionListingAndToggle(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	r := bashPPRunner(t, &out)
	bashPPRun(t, r, `shopt -s -o bashpp; shopt -o bashpp; shopt -p -o bashpp`)
	qt.Assert(t, qt.Equals(r.Dialect(), syntax.LangBashPP))
	qt.Assert(t, qt.IsTrue(strings.Contains(out.String(), "bashpp") && strings.Contains(out.String(), "\ton")),
		qt.Commentf("shopt -o output:\n%s", out.String()))
	qt.Assert(t, qt.IsTrue(strings.Contains(out.String(), "set -o bashpp")),
		qt.Commentf("shopt -p -o output:\n%s", out.String()))

	out.Reset()
	bashPPRun(t, r, `shopt -u -o bashpp; shopt -q -o bashpp; true`)
	qt.Assert(t, qt.Equals(r.Dialect(), syntax.LangBash))
}

// TestBashPPSetOptionReset proves Reset restores the construction-time dialect,
// discarding any runtime `set -o bashpp` / `set +o bashpp` toggle — the option
// is per-shell scratch state, not part of the runner's fixed configuration.
func TestBashPPSetOptionReset(t *testing.T) {
	t.Parallel()

	t.Run("enable then reset to bash", func(t *testing.T) {
		t.Parallel()
		var out strings.Builder
		r := bashPPRunner(t, &out)
		bashPPRun(t, r, `set -o bashpp`)
		qt.Assert(t, qt.Equals(r.Dialect(), syntax.LangBashPP))
		r.Reset()
		qt.Assert(t, qt.Equals(r.Dialect(), syntax.LangBash))
		qt.Assert(t, qt.ErrorIs(r.SetObject("OBJ", 1), interp.ErrObjectsUnsupported))
	})

	t.Run("disable then reset to bashpp", func(t *testing.T) {
		t.Parallel()
		var out strings.Builder
		r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
		bashPPRun(t, r, `set +o bashpp`)
		qt.Assert(t, qt.Equals(r.Dialect(), syntax.LangBash))
		r.Reset()
		qt.Assert(t, qt.Equals(r.Dialect(), syntax.LangBashPP))
		qt.Assert(t, qt.IsNil(r.SetObject("OBJ", 1)))
	})
}

// TestBashPPSetOptionSubshell proves clone semantics: a subshell inherits the
// parent's live bashpp state, and a toggle inside the subshell does not leak
// back out — exactly like every other shell option, since a subshell runs on a
// cloned runner.
func TestBashPPSetOptionSubshell(t *testing.T) {
	t.Parallel()

	t.Run("subshell inherits enabled state", func(t *testing.T) {
		t.Parallel()
		var out strings.Builder
		r := bashPPRunner(t, &out)
		bashPPRun(t, r, `set -o bashpp; ( set +o )`)
		qt.Assert(t, qt.IsTrue(strings.Contains(out.String(), "set -o bashpp")),
			qt.Commentf("subshell listing:\n%s", out.String()))
	})

	t.Run("subshell toggle does not leak to parent", func(t *testing.T) {
		t.Parallel()
		var out strings.Builder
		r := bashPPRunner(t, &out)
		// The subshell enables bashpp; the parent must be untouched after it.
		bashPPRun(t, r, `( set -o bashpp ); true`)
		qt.Assert(t, qt.Equals(r.Dialect(), syntax.LangBash))
		qt.Assert(t, qt.ErrorIs(r.SetObject("OBJ", 1), interp.ErrObjectsUnsupported))
	})
}

// TestBashPPSetOptionPosixOrthogonal proves the bashpp and posix toggles are
// independent: neither one moves the other, they compose in either order, and
// POSIX mode still withdraws objects while the bash++ dialect stays selected.
func TestBashPPSetOptionPosixOrthogonal(t *testing.T) {
	t.Parallel()

	t.Run("PosixMode getter tracks the option", func(t *testing.T) {
		t.Parallel()
		var out strings.Builder
		r := bashPPRunner(t, &out)
		qt.Assert(t, qt.IsFalse(r.PosixMode()))
		bashPPRun(t, r, `set -o posix`)
		qt.Assert(t, qt.IsTrue(r.PosixMode()))
		bashPPRun(t, r, `set +o posix`)
		qt.Assert(t, qt.IsFalse(r.PosixMode()))
	})

	t.Run("WithPosixMode seeds the getter", func(t *testing.T) {
		t.Parallel()
		var out strings.Builder
		r := bashPPRunner(t, &out, interp.WithPosixMode(true))
		qt.Assert(t, qt.IsTrue(r.PosixMode()))
	})

	t.Run("bashpp does not touch posix", func(t *testing.T) {
		t.Parallel()
		var out strings.Builder
		r := bashPPRunner(t, &out)
		bashPPRun(t, r, `set -o bashpp`)
		qt.Assert(t, qt.IsFalse(r.PosixMode()))
		qt.Assert(t, qt.Equals(r.Dialect(), syntax.LangBashPP))
	})

	t.Run("posix does not touch the dialect but withdraws objects", func(t *testing.T) {
		t.Parallel()
		var out strings.Builder
		r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
		// Entering POSIX mode keeps the bash++ dialect selected...
		bashPPRun(t, r, `set -o posix`)
		qt.Assert(t, qt.IsTrue(r.PosixMode()))
		qt.Assert(t, qt.Equals(r.Dialect(), syntax.LangBashPP))
		// ...but objects are off while POSIX mode promises a bare POSIX shell.
		qt.Assert(t, qt.ErrorIs(r.SetObject("OBJ", 1), interp.ErrObjectsUnsupported))
		// Leaving POSIX mode restores them, the dialect never having moved.
		bashPPRun(t, r, `set +o posix`)
		qt.Assert(t, qt.IsNil(r.SetObject("OBJ", 1)))
	})

	t.Run("both orders compose", func(t *testing.T) {
		t.Parallel()
		for _, script := range []string{
			`set -o posix; set -o bashpp`,
			`set -o bashpp; set -o posix`,
		} {
			var out strings.Builder
			r := bashPPRunner(t, &out)
			bashPPRun(t, r, script)
			qt.Check(t, qt.IsTrue(r.PosixMode()), qt.Commentf("script %q", script))
			qt.Check(t, qt.Equals(r.Dialect(), syntax.LangBashPP), qt.Commentf("script %q", script))
		}
	})
}
