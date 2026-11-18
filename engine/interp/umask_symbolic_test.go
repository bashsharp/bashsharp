// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

package interp

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-quicktest/qt"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// runUmaskPosix runs src through a fresh Runner started in POSIX mode
// (`set -o posix`, as `bash --posix`) and returns the combined
// stdout+stderr. The per-Runner virtual umask starts from a known value
// only after the script's own `umask` call, so each case is
// self-contained.
func runUmaskPosix(t *testing.T, src string) string {
	t.Helper()
	var buf bytes.Buffer
	r, err := New(Params("-o", "posix"), StdIO(strings.NewReader(""), &buf, &buf))
	qt.Assert(t, qt.IsNil(err))
	f, err := syntax.NewParser().Parse(strings.NewReader(src), "")
	qt.Assert(t, qt.IsNil(err))
	// Ignore the run error: non-zero exits are asserted via stderr text.
	_ = r.Run(context.Background(), f)
	return buf.String()
}

// TestUmaskSymbolic exercises chmod-style symbolic umask operands, the
// `umask -S` symbolic output, and the `-p`/`-S` flags. Every expected
// value matches `bash --posix` (and the yash tests/umask-p.tst POSIX
// suite, which previously had 58 symbolic cases failing because bashy's
// umask only accepted numeric masks).
//
// The grammar is one or more comma-separated `[who][op]perms` clauses:
// who ∈ {u,g,o,a} (default a), op ∈ {+,-,=}, perms ∈ {r,w,x} plus the
// copy tokens {u,g,o}. Operands mutate the current mask (relative to its
// granted-permission complement); a numeric operand replaces it. We read
// the result back with `umask -S` so the assertion is on the granted
// permissions, exactly as the yash suite measures via the created
// directory's mode.
func TestUmaskSymbolic(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		src  string
		want string
	}{
		// who=u, op=+ (yash: umask 777 then operand)
		{"u+empty", "umask 777; umask 'u+'; umask -S", "u=,g=,o=\n"},
		{"u+r", "umask 777; umask 'u+r'; umask -S", "u=r,g=,o=\n"},
		{"u+w", "umask 777; umask 'u+w'; umask -S", "u=w,g=,o=\n"},
		{"u+x", "umask 777; umask 'u+x'; umask -S", "u=x,g=,o=\n"},
		{"u+rw", "umask 777; umask 'u+rw'; umask -S", "u=rw,g=,o=\n"},
		{"u+xr", "umask 777; umask 'u+xr'; umask -S", "u=rx,g=,o=\n"},
		{"u+wx", "umask 777; umask 'u+wx'; umask -S", "u=wx,g=,o=\n"},
		{"u+xwr", "umask 777; umask 'u+xwr'; umask -S", "u=rwx,g=,o=\n"},

		// who=g, op=+
		{"g+empty", "umask 777; umask 'g+'; umask -S", "u=,g=,o=\n"},
		{"g+r", "umask 777; umask 'g+r'; umask -S", "u=,g=r,o=\n"},
		{"g+w", "umask 777; umask 'g+w'; umask -S", "u=,g=w,o=\n"},
		{"g+x", "umask 777; umask 'g+x'; umask -S", "u=,g=x,o=\n"},
		{"g+rw", "umask 777; umask 'g+rw'; umask -S", "u=,g=rw,o=\n"},
		{"g+xr", "umask 777; umask 'g+xr'; umask -S", "u=,g=rx,o=\n"},
		{"g+wx", "umask 777; umask 'g+wx'; umask -S", "u=,g=wx,o=\n"},
		{"g+xwr", "umask 777; umask 'g+xwr'; umask -S", "u=,g=rwx,o=\n"},

		// who=o, op=+
		{"o+empty", "umask 777; umask 'o+'; umask -S", "u=,g=,o=\n"},
		{"o+r", "umask 777; umask 'o+r'; umask -S", "u=,g=,o=r\n"},
		{"o+w", "umask 777; umask 'o+w'; umask -S", "u=,g=,o=w\n"},
		{"o+x", "umask 777; umask 'o+x'; umask -S", "u=,g=,o=x\n"},
		{"o+rw", "umask 777; umask 'o+rw'; umask -S", "u=,g=,o=rw\n"},
		{"o+xr", "umask 777; umask 'o+xr'; umask -S", "u=,g=,o=rx\n"},
		{"o+wx", "umask 777; umask 'o+wx'; umask -S", "u=,g=,o=wx\n"},
		{"o+xwr", "umask 777; umask 'o+xwr'; umask -S", "u=,g=,o=rwx\n"},

		// who=a (explicit)
		{"a+empty", "umask 777; umask 'a+'; umask -S", "u=,g=,o=\n"},
		{"a+r", "umask 777; umask 'a+r'; umask -S", "u=r,g=r,o=r\n"},
		{"a+w", "umask 777; umask 'a+w'; umask -S", "u=w,g=w,o=w\n"},
		{"a+x", "umask 777; umask 'a+x'; umask -S", "u=x,g=x,o=x\n"},
		{"a+rw", "umask 777; umask 'a+rw'; umask -S", "u=rw,g=rw,o=rw\n"},
		{"a+xr", "umask 777; umask 'a+xr'; umask -S", "u=rx,g=rx,o=rx\n"},
		{"a+wx", "umask 777; umask 'a+wx'; umask -S", "u=wx,g=wx,o=wx\n"},
		{"a+xwr", "umask 777; umask 'a+xwr'; umask -S", "u=rwx,g=rwx,o=rwx\n"},

		// who omitted (defaults to a)
		{"+empty", "umask 777; umask '+'; umask -S", "u=,g=,o=\n"},
		{"+r", "umask 777; umask '+r'; umask -S", "u=r,g=r,o=r\n"},
		{"+w", "umask 777; umask '+w'; umask -S", "u=w,g=w,o=w\n"},
		{"+x", "umask 777; umask '+x'; umask -S", "u=x,g=x,o=x\n"},
		{"+rw", "umask 777; umask '+rw'; umask -S", "u=rw,g=rw,o=rw\n"},
		{"+xr", "umask 777; umask '+xr'; umask -S", "u=rx,g=rx,o=rx\n"},
		{"+wx", "umask 777; umask '+wx'; umask -S", "u=wx,g=wx,o=wx\n"},
		{"+xwr", "umask 777; umask '+xwr'; umask -S", "u=rwx,g=rwx,o=rwx\n"},

		// op== (set, not add)
		{"u=empty", "umask 777; umask 'u='; umask -S", "u=,g=,o=\n"},
		{"u=r", "umask 777; umask 'u=r'; umask -S", "u=r,g=,o=\n"},
		{"u=w", "umask 777; umask 'u=w'; umask -S", "u=w,g=,o=\n"},
		{"u=x", "umask 777; umask 'u=x'; umask -S", "u=x,g=,o=\n"},
		{"u=rw", "umask 777; umask 'u=rw'; umask -S", "u=rw,g=,o=\n"},
		{"u=xr", "umask 777; umask 'u=xr'; umask -S", "u=rx,g=,o=\n"},
		{"u=wx", "umask 777; umask 'u=wx'; umask -S", "u=wx,g=,o=\n"},
		{"u=xwr", "umask 777; umask 'u=xwr'; umask -S", "u=rwx,g=,o=\n"},

		// chained operators inside one clause, applied left-to-right
		{"u=r+w", "umask 777; umask 'u=r+w'; umask -S", "u=rw,g=,o=\n"},
		{"u+w=r", "umask 777; umask 'u+w=r'; umask -S", "u=r,g=,o=\n"},
		{"u+w=r+x", "umask 777; umask 'u+w=r+x'; umask -S", "u=rx,g=,o=\n"},

		// multiple comma-separated clauses
		{"multi-clause", "umask 777; umask 'u=r+w,g=wx,o+xr'; umask -S", "u=rw,g=wx,o=rx\n"},
		{"u=rwx,u-w", "umask 777; umask 'u=rwx,u-w'; umask -S", "u=rx,g=,o=\n"},

		// permission-copy tokens (perms = another triad's granted bits)
		{"g=u", "umask 177; umask 'g=u'; umask -S", "u=rw,g=rw,o=\n"},
		{"o=u", "umask 177; umask 'o=u'; umask -S", "u=rw,g=,o=rw\n"},
		{"og=u", "umask 177; umask 'og=u'; umask -S", "u=rw,g=rw,o=rw\n"},
		{"g+u,o+rwx-u", "umask 177; umask 'g+u,o+rwx-u'; umask -S", "u=rw,g=rw,o=x\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := runUmaskPosix(t, tc.src)
			qt.Assert(t, qt.Equals(got, tc.want))
		})
	}
}

// TestUmaskNumericAndFlags covers numeric set/read, the -S and -p output
// forms, the documented "operand present → -S is ignored" rule, and the
// error diagnostics, all matching bash --posix.
func TestUmaskNumericAndFlags(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		src  string
		want string
	}{
		{"read-default-octal", "umask 022; umask", "0022\n"},
		{"read-octal-077", "umask 077; umask", "0077\n"},
		{"set-symbolic-then-read-octal", "umask 022; umask u=r+w; umask", "0122\n"},
		{"S-output", "umask 022; umask -S", "u=rwx,g=rx,o=rx\n"},
		{"p-output", "umask 022; umask -p", "umask 0022\n"},
		{"p-and-S-output", "umask 022; umask -p -S", "umask -S u=rwx,g=rx,o=rx\n"},
		// With an operand, -S is ignored: the mask is set and nothing prints.
		{"S-ignored-with-operand", "umask -S 000; umask", "0000\n"},
		// Errors.
		{"octal-out-of-range", "umask 999", "umask: 999: octal number out of range\n"},
		{"invalid-option", "umask -i", "umask: -i: invalid option\numask: usage: umask [-p] [-S] [mode]\n"},
		{"invalid-symbolic-char", "umask 'u+q'", "umask: `q': invalid symbolic mode character\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := runUmaskPosix(t, tc.src)
			qt.Assert(t, qt.Equals(got, tc.want))
		})
	}
}

// TestRedirectFileModeBase verifies that a file-creating redirect uses
// base mode 0666 (as bash does), not 0644 — so a group/other-write bit
// the umask permits survives. Regression for VSC-PCTS #378, where
// `umask 025; cat > f` created 0640 instead of bash's 0642 because the
// redirect open passed 0644.
func TestRedirectFileModeBase(t *testing.T) {
	dir := t.TempDir()
	// The kernel also applies this process's real umask on top of the
	// Runner's virtual mask, so fold it into the expectation rather than
	// mutating the process-wide umask (which would race other tests).
	proc := processUmask()
	for i, mask := range []int{0, 0o002, 0o022, 0o025, 0o026, 0o077} {
		name := filepath.Join(dir, fmt.Sprintf("f%d", i))
		src := fmt.Sprintf("umask %04o; : > %q", mask, name)
		f, err := syntax.NewParser().Parse(strings.NewReader(src), "")
		qt.Assert(t, qt.IsNil(err))
		r, err := New(StdIO(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}))
		qt.Assert(t, qt.IsNil(err))
		qt.Assert(t, qt.IsNil(r.Run(context.Background(), f)))
		info, err := os.Stat(name)
		qt.Assert(t, qt.IsNil(err))
		want := os.FileMode(0o666 &^ mask &^ proc)
		qt.Check(t, qt.Equals(info.Mode().Perm(), want),
			qt.Commentf("umask %04o", mask))
	}
}

// TestUmaskRestoreRoundTrip mirrors yash's test_restore_{non_,}symbolic:
// the output of `umask` / `umask -S` must be a valid operand that
// restores the same mask. We verify the round-trip for several masks.
func TestUmaskRestoreRoundTrip(t *testing.T) {
	t.Parallel()
	for _, mask := range []string{"000", "001", "002", "022", "077", "653", "017", "777"} {
		mask := mask
		t.Run("numeric-"+mask, func(t *testing.T) {
			t.Parallel()
			src := "umask " + mask + `; m=$(umask); umask 777; umask "$m"; test "$m" = "$(umask)" && echo SAME`
			qt.Assert(t, qt.Equals(runUmaskPosix(t, src), "SAME\n"))
		})
		t.Run("symbolic-"+mask, func(t *testing.T) {
			t.Parallel()
			src := "umask " + mask + `; m=$(umask -S); umask 777; umask "$m"; test "$m" = "$(umask -S)" && echo SAME`
			qt.Assert(t, qt.Equals(runUmaskPosix(t, src), "SAME\n"))
		})
	}
}
