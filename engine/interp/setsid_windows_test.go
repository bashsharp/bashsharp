// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

//go:build windows

package interp

import (
	"bytes"
	"os"
	"testing"
)

func TestWindowsDetachedExecCmd(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := &Runner{
		Dir:    `C:\work`,
		stdin:  os.Stdin,
		stdout: &stdout,
		stderr: &stderr,
	}

	cmd := detachedExecCmd(r, `C:\Windows\System32\cmd.exe`, []string{"cmd", "/c", "exit", "0"})
	if cmd.SysProcAttr == nil {
		t.Fatal("SysProcAttr is nil")
	}
	if got := cmd.SysProcAttr.CreationFlags; got&windowsDetachedProcFlags != windowsDetachedProcFlags {
		t.Fatalf("CreationFlags = %#x, want DETACHED_PROCESS|CREATE_NEW_PROCESS_GROUP", got)
	}
	if cmd.Stdin != os.Stdin {
		t.Fatalf("Stdin = %p, want %p", cmd.Stdin, os.Stdin)
	}
	if cmd.Stdout != &stdout {
		t.Fatalf("Stdout = %p, want %p", cmd.Stdout, &stdout)
	}
	if cmd.Stderr != &stderr {
		t.Fatalf("Stderr = %p, want %p", cmd.Stderr, &stderr)
	}
	if cmd.Dir != `C:\work` {
		t.Fatalf("Dir = %q, want C:\\work", cmd.Dir)
	}
}
