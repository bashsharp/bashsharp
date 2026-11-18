// Copyright (c) 2026 qiangli
// See LICENSE for licensing information

//go:build linux

package cli

import (
	"io"
	"syscall"
	"testing"
)

// TestTP714FaultSignalIgnoreThenTrap recreates the VSC case-1 process
// boundary: a runtime-owned fault signal is first ignored, then trapped, and
// finally delivered externally while the standalone shell blocks in read.
func TestTP714FaultSignalIgnoreThenTrap(t *testing.T) {
	tests := []struct {
		name string
		sig  syscall.Signal
	}{
		{"BUS", syscall.SIGBUS},
		{"FPE", syscall.SIGFPE},
		{"ILL", syscall.SIGILL},
		{"SEGV", syscall.SIGSEGV},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := signalShell(t, "trap '' "+tc.name+"; trap 'echo caught' "+tc.name+"; echo ready; read x; echo got=$x")
			p.wantLine(t, "ready")
			if err := p.cmd.Process.Signal(tc.sig); err != nil {
				t.Fatal(err)
			}
			p.wantLine(t, "caught")
			if _, err := io.WriteString(p.stdin, "input\n"); err != nil {
				t.Fatal(err)
			}
			p.wantLine(t, "got=input")
			p.finish(t)
		})
	}
}
