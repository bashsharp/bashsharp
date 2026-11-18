// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

//go:build !plan9 && !js

package interactive

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/go-quicktest/qt"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// TestPOSIXInteractiveShiftErrorReprompts covers VSC-PCTS TP709's exact two
// disputed commands. An interactive POSIX shell must preserve its positional
// parameters, return a non-zero command status, and continue to the next
// prompt after both an excessive and an invalid shift count.
func TestPOSIXInteractiveShiftErrorReprompts(t *testing.T) {
	input := strings.NewReader("shift 4\necho first:$? count:$# args:$*\nshift '('\necho second:$? count:$# args:$*\nexit 0\n")
	var stdout, stderr bytes.Buffer
	r, err := interp.New(
		interp.Params("-o", "posix", "--", "one", "two", "three"),
		interp.Interactive(true),
		interp.WithBashCompatErrors(true),
		interp.WithArgv0("sh"),
		interp.StdIO(strings.NewReader(""), &stdout, &stderr),
	)
	qt.Assert(t, qt.IsNil(err))

	// Exercise the plain interactive loop directly. Its statement/error/
	// reprompt control flow is shared with the terminal-backed loop, without
	// involving terminal probing or line-editor echo in this hermetic test.
	err = runFallback(context.Background(), r, input, &stdout, &stderr,
		syntax.LangBash, true,
		func() string { return "PROMPT> " },
		func() string { return "> " },
		func(error) {}, nil, nil, nil)
	qt.Assert(t, qt.IsNil(err))

	qt.Check(t, qt.StringContains(stdout.String(), "first:1 count:3 args:one two three"))
	qt.Check(t, qt.StringContains(stdout.String(), "second:2 count:3 args:one two three"))
	qt.Check(t, qt.Equals(strings.Count(stdout.String(), "PROMPT> "), 5),
		qt.Commentf("every input command, including both failed shifts, must be followed by a prompt; stdout=%q", stdout.String()))
	qt.Check(t, qt.StringContains(stderr.String(), "shift: 4: shift count out of range"))
	qt.Check(t, qt.StringContains(stderr.String(), "shift: (: numeric argument required"))
}
