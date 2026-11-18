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

func TestAssumeTTYRejectsMalformedWholeLineWithoutLangFunc(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r, err := interp.New(
		interp.Interactive(true),
		interp.StdIO(strings.NewReader(""), &stdout, &stderr),
	)
	qt.Assert(t, qt.IsNil(err))

	err = Run(context.Background(), Options{
		Runner:    r,
		Lang:      syntax.LangBash,
		AssumeTTY: true,
		Stdin:     strings.NewReader("echo prefix; )\recho continued\rexit 0\r"),
		Stdout:    io.Discard,
		Stderr:    &stderr,
	})
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(stdout.String(), "continued\n"))
	qt.Check(t, qt.Not(qt.Equals(stderr.String(), "")))
}
