// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

//go:build windows

package interp

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/term"
)

// runNohup implements the `nohup` builtin on Windows. Windows has no SIGHUP,
// but launching through runDetachedExec gives the child a detached process
// context and a fresh process group. Stdio follows the same rules as Unix:
// terminal stdin is replaced with os.DevNull, terminal stdout goes to
// nohup.out, and terminal stderr follows stdout.
func (r *Runner) runNohup(ctx context.Context, args []string) exitStatus {
	var exit exitStatus
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		r.errf("nohup: usage: nohup <program> [args...]\n")
		exit.code = 125
		return exit
	}

	origStdin := r.stdin
	origStdout := r.stdout
	origStderr := r.stderr
	defer func() {
		r.stdin = origStdin
		r.stdout = origStdout
		r.stderr = origStderr
	}()

	if isTTYFile(origStdin) {
		f, err := os.Open(os.DevNull)
		if err != nil {
			r.errf("nohup: open %s: %v\n", os.DevNull, err)
			exit.code = 125
			return exit
		}
		defer f.Close()
		r.stdin = f
	}

	var nohupOut *os.File
	stdoutIsTTY := isTTYWriter(origStdout)
	if stdoutIsTTY {
		f, path, err := openNohupOut(r)
		if err != nil {
			r.errf("nohup: %v\n", err)
			exit.code = 125
			return exit
		}
		defer f.Close()
		nohupOut = f
		r.stdout = f
		fmt.Fprintf(r.bashPPWriter(origStderr), "nohup: appending output to %q\n", path)
	}

	if isTTYWriter(origStderr) {
		if nohupOut != nil {
			r.stderr = nohupOut
		} else {
			r.stderr = r.stdout
		}
	}

	return runDetachedExec(ctx, r, "nohup", args, true /*foreground*/)
}

func openNohupOut(r *Runner) (*os.File, string, error) {
	path := filepath.Join(r.Dir, "nohup.out")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err == nil {
		return f, path, nil
	}
	home := r.envGet("HOME")
	if home == "" {
		return nil, "", fmt.Errorf("cannot open %s and HOME is unset: %v", path, err)
	}
	path = filepath.Join(home, "nohup.out")
	f, err = os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return nil, "", fmt.Errorf("cannot open %s: %v", path, err)
	}
	return f, path, nil
}

func isTTYFile(f *os.File) bool {
	if f == nil {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

func isTTYWriter(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}
