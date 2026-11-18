// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

//go:build !plan9 && !js

package interactive

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/go-quicktest/qt"
)

// A router sees each fresh line once: an unchanged line keeps shell
// semantics (continuation included), a replaced line runs as typed, and ""
// skips the line.
func TestRouteRewritesSkipsAndKeepsLines(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r, err := interp.New(
		interp.Interactive(true),
		interp.StdIO(strings.NewReader(""), &stdout, &stderr),
	)
	qt.Assert(t, qt.IsNil(err))

	var seen []string
	err = Run(context.Background(), Options{
		Runner:    r,
		Lang:      syntax.LangBash,
		AssumeTTY: true,
		Stdin:     strings.NewReader("echo 'kept\rline'\rwhat's this?\rskip me\rexit 0\r"),
		Stdout:    io.Discard,
		Stderr:    &stderr,
		Route: func(_ context.Context, _ *interp.Runner, line string) string {
			seen = append(seen, line)
			switch {
			case strings.HasPrefix(line, "what"):
				return "echo turn"
			case strings.HasPrefix(line, "skip"):
				return ""
			}
			return line
		},
	})
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(stdout.String(), "kept\nline\nturn\n"))
	qt.Check(t, qt.DeepEquals(seen, []string{"echo 'kept", "what's this?", "skip me", "exit 0"}))
}
