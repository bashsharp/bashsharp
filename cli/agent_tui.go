package cli

import (
	"os"
	"os/exec"
	"strings"
)

// `bashy ycode` on a terminal hosts ycode's native TUI (Sprint #387 A7); its
// action ladder (pkg/ladder) runs literal lines through LiteralCommand. The
// in-shell agent terminal (BASHY_AGENT_TUI) it replaced is retired.

// LiteralCommand is the process that runs one literal line typed in ycode's
// native TUI (rung 0, or the rung-1 repaired line the TUI echoed): the bashy
// binary itself as `bashy -c LINE`, the line passed through byte-identically.
// The caller (the TUI) wires stdin/stdout/stderr to the terminal. Wired as
// ycodecli.LiteralShell by cmd/bashy.
func LiteralCommand(line string) *exec.Cmd {
	self, err := selfExecutable()
	if err != nil {
		self = os.Args[0]
	}
	cmd := exec.Command(self, "-c", line)
	// The launching process already reported telemetry once.
	cmd.Env = append(os.Environ(), "BASHY_TELEMETRY_QUIET=1")
	return cmd
}

// selfExecutable prefers the unix launcher over the Go program it execs
// (bin/bashy over bin/bashy.real) so the shell keeps its signal snapshot.
func selfExecutable() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	if launcher, ok := strings.CutSuffix(self, ".real"); ok {
		if _, err := os.Stat(launcher); err == nil {
			return launcher, nil
		}
	}
	return self, nil
}
