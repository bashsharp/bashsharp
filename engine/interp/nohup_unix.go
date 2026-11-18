// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

//go:build unix

package interp

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/term"
)

// runNohup implements the `nohup` builtin. The POSIX semantics are:
//   - if stdin is a tty,  redirect from /dev/null
//   - if stdout is a tty, redirect to ./nohup.out (or $HOME/nohup.out)
//   - if stderr is a tty, redirect to whatever stdout went to
//
// In addition to those redirections, the spawned program is launched in a
// new session (SysProcAttr.Setsid = true) via runDetachedExec, so it
// survives the parent shell (i.e. the outpost SSH session) being torn down.
// Bash/coreutils don't explicitly setsid; they rely on the user's `&`
// statement leaving the child in the parent's process group and dodging
// SIGHUP via signal masking. In the outpost matrix-shell, the parent is
// the outpost process itself, and Setsid on the child is more reliable
// than depending on SIGHUP-handling guarantees that may not exist.
//
// Limitation: when both stdin AND stdout are pipes/sockets (e.g. invoked
// over a non-tty ssh exec like `ssh host 'nohup foo &'`), POSIX says to
// inherit them. The child will then die on SIGPIPE when the ssh channel
// closes. Users in that scenario should explicitly redirect:
// `nohup foo > /tmp/out 2>&1 &`.
func (r *Runner) runNohup(ctx context.Context, args []string) exitStatus {
	var exit exitStatus
	// Strip `--` only — nohup has no real flags.
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
		// Match coreutils: print the path to stderr so users know where it went.
		fmt.Fprintf(r.bashPPWriter(origStderr), "nohup: appending output to %q\n", path)
	}

	if isTTYWriter(origStderr) {
		if nohupOut != nil {
			r.stderr = nohupOut
		} else {
			// stderr was a tty but stdout was not — POSIX: redirect to stdout
			r.stderr = r.stdout
		}
	}

	return runDetachedExec(ctx, r, "nohup", args, true /*foreground*/)
}

// openNohupOut opens ./nohup.out append-mode 0600 in cwd, falling back to
// $HOME/nohup.out if cwd isn't writable.
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

// isTTYFile reports whether f is a terminal. nil files are not terminals.
func isTTYFile(f *os.File) bool {
	if f == nil {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// isTTYWriter reports whether w is an *os.File that is a terminal. Anything
// that isn't a real OS file (bytes.Buffer in tests, ssh.Channel in outpost)
// is treated as not-a-tty.
func isTTYWriter(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}
