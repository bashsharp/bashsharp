// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

//go:build unix

package interp

import (
	"context"
	"os/exec"
	"syscall"
)

// runSetsid implements the `setsid` builtin. It looks up the program in PATH
// and execs it with a fresh session (SysProcAttr.Setsid = true) so the child
// becomes its own session leader and is detached from any controlling
// terminal of the caller.
//
// Flags accepted (POSIX-ish):
//   - -f, -w, -c   no-ops (we always exec, always wait, never set ctty)
func (r *Runner) runSetsid(ctx context.Context, args []string) exitStatus {
	var exit exitStatus
parseFlags:
	for len(args) > 0 {
		switch args[0] {
		case "-f", "-w", "-c":
			args = args[1:]
		case "--":
			args = args[1:]
			break parseFlags
		default:
			if len(args[0]) > 0 && args[0][0] == '-' {
				r.errf("setsid: invalid option: %q\n", args[0])
				exit.code = 2
				return exit
			}
			break parseFlags
		}
	}
	if len(args) == 0 {
		r.errf("setsid: usage: setsid [-f] [-w] [-c] <program> [args...]\n")
		exit.code = 2
		return exit
	}
	return runDetachedExec(ctx, r, "setsid", args, true /*foreground*/)
}

// runDetachedExec spawns args[0] with the rest of args, in a new session
// (SysProcAttr.Setsid = true). Used by both `setsid` and `nohup`. If
// foreground is true, the parent waits for the child and returns its exit
// status; if false, the parent returns immediately (exit 0) and the child
// is genuinely backgrounded — typically the caller has already piped stdio
// through a non-tty target in that case.
//
// stdin/stdout/stderr come from the runner's current redirections, which
// the caller (the nohup builtin) may have already replaced with a /dev/null
// reader and a nohup.out writer respectively.
func runDetachedExec(ctx context.Context, r *Runner, label string, args []string, foreground bool) exitStatus {
	var exit exitStatus
	path, err := LookPathDir(r.Dir, r.writeEnv, args[0])
	if err != nil {
		r.errf("%s: %v\n", label, err)
		exit.code = 127
		return exit
	}
	cmd := exec.Cmd{
		Path:   path,
		Args:   args,
		Env:    execEnv(r.bashPPEnv()),
		Dir:    r.Dir,
		Stdin:  r.stdin,
		Stdout: r.bashPPWriter(r.stdout),
		Stderr: r.bashPPWriter(r.stderr),
		SysProcAttr: &syscall.SysProcAttr{
			Setsid: true,
		},
	}
	if err := startExecCmdWithinBudget(&cmd); err != nil {
		r.errf("%s: %v\n", label, err)
		// 126 = found but not executable, per POSIX
		exit.code = 126
		return exit
	}
	publishBgPid(ctx, cmd.Process.Pid)

	if !foreground {
		// Detached: the child now owns its own session. We deliberately do
		// NOT wait — the shell statement that ran the builtin returns
		// immediately. This mirrors what `setsid -f cmd &` does in bash on
		// systems where /usr/bin/setsid exists.
		return exit
	}

	// Deliberately NOT installing a context.AfterFunc that kills the child
	// on ctx cancel: the whole point of nohup/setsid is that the child
	// survives the parent shell going away. The runner's context is
	// cancelled when the SSH session ends (or outpost is shutting down),
	// and we want the detached child to keep running anyway. The parent
	// goroutine just blocks on cmd.Wait() and returns naturally when the
	// child exits on its own — which may be hours later. That's fine; the
	// outpost daemon is long-lived and the goroutine leak is bounded by
	// the child's actual lifetime.
	//
	// Side effect: SIGINT (^C) on the matrix-shell session can't reach a
	// nohup'd child because it's now in a different session/PGID. That
	// matches POSIX nohup / setsid semantics.

	if err := cmd.Wait(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				exit.code = 128 + uint8(ws.Signal())
				return exit
			}
			exit.code = uint8(ee.ExitCode())
			return exit
		}
		r.errf("%s: %v\n", label, err)
		exit.code = 1
	}
	return exit
}
