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
	"github.com/ergochat/readline"
	"github.com/go-quicktest/qt"
)

func TestPlainTerminalResolvesLiveBashPPInPosixMode(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r, err := interp.New(
		interp.Lang(syntax.LangBash),
		interp.WithPosixMode(true),
		interp.StdIO(strings.NewReader(""), &stdout, &stderr),
	)
	qt.Assert(t, qt.IsNil(err))

	err = Run(context.Background(), Options{
		Runner:        r,
		Stdin:         strings.NewReader("set -o bashpp; var x = 1; echo $x; exit 0\n"),
		Stdout:        &stdout,
		Stderr:        &stderr,
		PlainTerminal: true,
		PosixMode:     true,
		LangFunc:      func(r *interp.Runner) syntax.LangVariant { return r.Dialect() },
	})
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.StringContains(stderr.String(), "bash++ declaration evaluated with extensions disabled"), qt.Commentf("stdout=%q stderr=%q", stdout.String(), stderr.String()))
	qt.Check(t, qt.Not(qt.StringContains(stderr.String(), "command not found")), qt.Commentf("stdout=%q stderr=%q", stdout.String(), stderr.String()))
}

func TestConfigureReadlineRunsBeforeConstruction(t *testing.T) {
	r, err := interp.New(interp.Interactive(true), interp.StdIO(strings.NewReader(""), io.Discard, io.Discard))
	qt.Assert(t, qt.IsNil(err))

	called := false
	err = Run(context.Background(), Options{
		Runner: r,
		Stdin:  strings.NewReader(""),
		Stdout: io.Discard,
		Stderr: io.Discard,
		ConfigureReadline: func(cfg *readline.Config) {
			called = true
			cfg.Undo = true
		},
	})
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsTrue(called))
}

func TestPlainTerminalSkipsReadlineConfiguration(t *testing.T) {
	r, err := interp.New(interp.Interactive(true), interp.StdIO(strings.NewReader(""), io.Discard, io.Discard))
	qt.Assert(t, qt.IsNil(err))

	err = Run(context.Background(), Options{
		Runner:        r,
		Stdin:         strings.NewReader(""),
		Stdout:        io.Discard,
		Stderr:        io.Discard,
		PlainTerminal: true,
		ConfigureReadline: func(*readline.Config) {
			t.Fatal("plain terminal unexpectedly constructed readline")
		},
	})
	qt.Assert(t, qt.IsNil(err))
}
