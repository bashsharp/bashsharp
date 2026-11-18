// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

//go:build plan9 || js

// Package interactive runs a fallback interactive shell loop on top of an
// [*interp.Runner] on platforms where the readline dependency is unavailable.
package interactive

import (
	"context"
	"errors"
	"io"
	"os"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Options configures [Run].
type Options struct {
	// Runner is the in-process shell interpreter to drive. Required.
	Runner *interp.Runner

	// Lang selects the parser variant (Bash, POSIX, mksh, ...). Zero
	// defaults to [syntax.LangBash].
	Lang syntax.LangVariant

	// Stdin is the input source.
	Stdin io.Reader

	// AssumeTTY is ignored by this fallback implementation.
	AssumeTTY bool
	// GetSize is ignored by this fallback implementation.
	GetSize func() (cols, rows int)
	// Stdout receives prompts and command stdout. If nil, [os.Stdout] is used.
	Stdout io.Writer
	// Stderr receives runner diagnostics. If nil, [os.Stderr] is used.
	Stderr io.Writer

	// PS1 / PS2 return the prompt to display before each new line of input.
	// Nil defaults: PS1 = "$ ", PS2 = "> ".
	PS1 func() string
	PS2 func() string

	// PreCommand, if non-nil, is invoked before each PS1 prompt is shown.
	PreCommand func(context.Context, *interp.Runner)

	// OnRunError is called when a parsed statement returns a non-nil error
	// that is not just a non-zero command exit code.
	OnRunError func(error)

	// Greeting is written verbatim to Stdout once before the first prompt.
	Greeting string

	// History options are ignored by this fallback implementation.
	HistoryFile       string
	HistoryLimit      int
	HistorySearchFold bool
	InterruptPrompt   string
	EOFPrompt         string

	// OnEOF is ignored by this fallback implementation; EOF exits cleanly.
	OnEOF func() bool
}

// Run starts a plain newline-buffered interactive shell loop.
func Run(ctx context.Context, opts Options) error {
	if opts.Runner == nil {
		return errors.New("interactive: Runner is required")
	}
	stdout := opts.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	stderr := opts.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	stdin := opts.Stdin
	if stdin == nil {
		stdin = os.Stdin
	}
	lang := opts.Lang
	if lang == 0 {
		lang = syntax.LangBash
	}
	ps1 := opts.PS1
	if ps1 == nil {
		ps1 = func() string { return "$ " }
	}
	ps2 := opts.PS2
	if ps2 == nil {
		ps2 = func() string { return "> " }
	}
	onRunError := opts.OnRunError
	if onRunError == nil {
		onRunError = func(err error) { _, _ = io.WriteString(stderr, err.Error()+"\n") }
	}
	if opts.Greeting != "" {
		_, _ = io.WriteString(stdout, opts.Greeting)
	}
	return runFallback(ctx, opts.Runner, stdin, stdout, stderr, lang, ps1, ps2, onRunError, opts.PreCommand)
}

func runFallback(ctx context.Context, r *interp.Runner, stdin io.Reader, stdout, stderr io.Writer, lang syntax.LangVariant, ps1, ps2 func() string, onRunError func(error), preCommand func(context.Context, *interp.Runner)) error {
	parser := syntax.NewParser(syntax.Variant(lang))
	if preCommand != nil {
		preCommand(ctx, r)
	}
	_, _ = io.WriteString(stdout, ps1())
	for stmts, err := range parser.InteractiveSeq(stdin) {
		if err != nil {
			_, _ = io.WriteString(stderr, err.Error()+"\n")
			return err
		}
		if parser.Incomplete() {
			_, _ = io.WriteString(stdout, ps2())
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		for _, stmt := range stmts {
			cmdCtx, cancel := context.WithCancel(ctx)
			runErr := r.Run(cmdCtx, stmt)
			cancel()
			if runErr != nil && !isExitStatus(runErr) {
				onRunError(runErr)
			}
			if r.Exited() {
				return runErr
			}
		}
		if preCommand != nil {
			preCommand(ctx, r)
		}
		_, _ = io.WriteString(stdout, ps1())
	}
	return nil
}

func isExitStatus(err error) bool {
	var ec interp.ExitStatus
	return errors.As(err, &ec)
}
