// Copyright (c) 2026 qiangli
// See LICENSE for licensing information

//go:build linux

package cli

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
)

func TestStandaloneFaultSignalsUseNativeWaitStatus(t *testing.T) {
	bashBin := os.Getenv("BASHY_TEST_STANDALONE_BIN")
	if bashBin == "" {
		bashBin = builtBashBin(t)
	}
	for _, sig := range []syscall.Signal{
		syscall.SIGBUS,
		syscall.SIGFPE,
		syscall.SIGILL,
		syscall.SIGSEGV,
		syscall.SIGTRAP,
	} {
		t.Run(sig.String(), func(t *testing.T) {
			cmd := exec.Command(bashBin, "-c", "echo ready; read value")
			cmd.Env = []string{"PATH=/bin:/usr/bin", "HOME=" + t.TempDir()}
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer stdin.Close()
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "ready\n" {
				t.Fatalf("readiness = %q, %v", line, err)
			}
			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			err = cmd.Wait()
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("wait error = %v, want signal death", err)
			}
			status, ok := exitErr.Sys().(syscall.WaitStatus)
			if !ok || !status.Signaled() || status.Signal() != sig {
				t.Fatalf("wait status = %#v, want signal %s", exitErr.Sys(), sig)
			}
			if stderr.Len() != 0 {
				t.Fatalf("fault signal emitted diagnostics: %q", stderr.String())
			}
		})
	}
}
