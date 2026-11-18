// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

package interp

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/go-quicktest/qt"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// TestInteractiveSpecialBuiltinErrorNonFatal covers VSC-PCTS #643: POSIX
// 2.8.1 makes an error in a special built-in abort a *non-interactive*
// shell, but an interactive shell must report it and carry on. Typing
// `unset` of a readonly variable at the prompt previously killed the whole
// bashy session (the interactive job-control assertion drives exactly that
// through `expect`).
func TestInteractiveSpecialBuiltinErrorNonFatal(t *testing.T) {
	run := func(t *testing.T, interactive bool) string {
		t.Helper()
		var buf bytes.Buffer
		opts := []RunnerOption{
			Params("-o", "posix"),
			StdIO(strings.NewReader(""), &buf, &buf),
		}
		if interactive {
			opts = append(opts, Interactive(true))
		}
		r, err := New(opts...)
		qt.Assert(t, qt.IsNil(err))
		f, err := syntax.NewParser().Parse(
			strings.NewReader("readonly x=1\nunset x\necho AFTER\n"), "")
		qt.Assert(t, qt.IsNil(err))
		_ = r.Run(context.Background(), f)
		return buf.String()
	}

	// Interactive: the readonly-unset error is reported but the shell keeps
	// running, so the following command executes.
	got := run(t, true)
	qt.Check(t, qt.StringContains(got, "cannot unset"))
	qt.Check(t, qt.StringContains(got, "AFTER"))

	// Non-interactive: the special-built-in error is fatal, so the shell
	// exits before the following command.
	got = run(t, false)
	qt.Check(t, qt.StringContains(got, "cannot unset"))
	qt.Check(t, qt.IsFalse(strings.Contains(got, "AFTER")),
		qt.Commentf("non-interactive shell should have exited: %q", got))
}

func TestInteractiveSpecialBuiltinShiftAndEvalNonFatal(t *testing.T) {
	run := func(t *testing.T, interactive bool, src string) (string, bool) {
		t.Helper()
		var buf bytes.Buffer
		opts := []RunnerOption{
			Params("-o", "posix", "--", "one", "two", "three"),
			StdIO(strings.NewReader(""), &buf, &buf),
			WithBashCompatErrors(true),
			WithArgv0("sh"),
		}
		if interactive {
			opts = append(opts, Interactive(true))
		}
		r, err := New(opts...)
		qt.Assert(t, qt.IsNil(err))
		f, err := syntax.NewParser().Parse(strings.NewReader(src), "")
		qt.Assert(t, qt.IsNil(err))
		_ = r.Run(context.Background(), f)
		return buf.String(), r.Exited()
	}

	// VSC-PCTS #709_2: an invalid shift operand fails the command, but an
	// interactive POSIX shell keeps the original positional parameters and
	// continues reading.
	got, exited := run(t, true, "shift '('\necho status:$? count:$# args:$*\n")
	qt.Check(t, qt.StringContains(got, "shift: (: numeric argument required"))
	qt.Check(t, qt.StringContains(got, "status:2 count:3 args:one two three"))
	qt.Check(t, qt.IsFalse(exited))

	// VSC-PCTS #739: an eval parse error in an interactive POSIX shell gives
	// eval a non-zero status; it must not exit the shell.
	got, exited = run(t, true, "eval ')'\necho status:$?\n")
	qt.Check(t, qt.StringContains(got, "eval: line 1:"))
	qt.Check(t, qt.StringContains(got, "status:1"))
	qt.Check(t, qt.IsFalse(exited))

	// Non-interactive POSIX mode remains fatal for the same eval parse error.
	_, exited = run(t, false, "eval ')'\necho status:$?\n")
	qt.Check(t, qt.IsTrue(exited))
}
