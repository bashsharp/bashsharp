//go:build unix

package cli

import (
	"os"
	"syscall"

	"golang.org/x/term"
)

// maybeNewProcessGroup puts this process into a new process group when the
// BASH_SETPGRP environment variable is set. The bash 5.3 test harness sets it
// so a per-fixture watchdog can reap the entire fixture process tree with
// `kill -- -<pid>` (a negative pid targets the whole process group), without
// depending on an external `setsid`/`perl` wrapper — a pure-Go shell should be
// testable with pure-Go tooling alone. Outside the harness the variable is
// unset and this is a harmless no-op.
//
// Setpgid(0, 0) makes this process a new group leader (the new pgid equals our
// pid). Errors (e.g. already a group leader) are ignored.
func maybeNewProcessGroup() {
	if os.Getenv("BASH_SETPGRP") == "" {
		return
	}
	// A shell attached to a terminal must remain in the terminal's foreground
	// process group. The non-TTY bash fixture harness needs this opt-in for its
	// watchdog, but the VSC runner already places the shell beneath a
	// foreground PTY wrapper. Detaching from that group without also taking
	// terminal ownership makes ioctls such as stty stop on SIGTTOU.
	if term.IsTerminal(int(os.Stdin.Fd())) || term.IsTerminal(int(os.Stdout.Fd())) {
		return
	}
	_ = syscall.Setpgid(0, 0)
}
