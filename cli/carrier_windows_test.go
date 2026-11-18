// Copyright (c) 2026 qiangli
// See LICENSE for licensing information

//go:build windows

package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
	"github.com/qiangli/bash/interp"
	"github.com/qiangli/bash/syntax"
)

// stillActiveExitCode is GetExitCodeProcess's "has not exited" pseudo code
// (STATUS_PENDING), which x/sys/windows does not export.
const stillActiveExitCode = 259

// TestWindowsJobCarrierPublishesLivePid pins the whole point of a carrier on
// Windows: a background job gets a real process id that an outside tool can
// find. This is what `kill -0 $!` walks, and what the POSIX suite compares as
// an integer.
func TestWindowsJobCarrierPublishesLivePid(t *testing.T) {
	cp, err := winJobCarrier{}.StartCarrier(context.Background())
	if err != nil {
		t.Fatalf("StartCarrier: %v", err)
	}
	pid := cp.Pid()
	if pid <= 0 {
		t.Fatalf("carrier pid = %d, want positive", pid)
	}
	if !carrierPidAlive(pid) {
		t.Fatalf("carrier pid %d names no live process", pid)
	}
	cp.Terminate()
	if sig := waitCarrier(t, cp); sig != 0 {
		t.Fatalf("Wait after Terminate = %d, want 0 (an ordinary exit, not a signal death)", sig)
	}
	if carrierPidAlive(pid) {
		t.Fatalf("carrier pid %d still live after Terminate+Wait", pid)
	}
}

// TestWindowsJobCarrierReportsSignalDeath pins the recovery of the signal from
// a Windows exit code: `kill $!` ends the carrier with bashy's marker, and
// Wait must report that signal number so the runner can relay it to the job.
func TestWindowsJobCarrierReportsSignalDeath(t *testing.T) {
	for _, num := range []int{windowsSigTERMForTest, windowsSigUSR1ForTest} {
		cp, err := winJobCarrier{}.StartCarrier(context.Background())
		if err != nil {
			t.Fatalf("StartCarrier: %v", err)
		}
		// Exactly what interp's sendSignal does to this pid on Windows.
		h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(cp.Pid()))
		if err != nil {
			cp.Terminate()
			t.Fatalf("OpenProcess(%d): %v", cp.Pid(), err)
		}
		err = windows.TerminateProcess(h, uint32(interp.SignalMarkerExitCode(num)))
		windows.CloseHandle(h)
		if err != nil {
			cp.Terminate()
			t.Fatalf("TerminateProcess(%d): %v", cp.Pid(), err)
		}
		if sig := waitCarrier(t, cp); sig != num {
			t.Fatalf("Wait after signal %d = %d, want %d", num, sig, num)
		}
	}
}

// Cygwin/MSYS numbering, the table interp uses on Windows. Spelled out here so
// the test pins the round trip rather than reusing whatever interp computed.
const (
	windowsSigTERMForTest = 15
	windowsSigUSR1ForTest = 30
)

// TestWindowsJobCarrierExitsOnStdinEOF pins the lifetime bound the carrier
// protocol relies on: the helper's stdin is a parent-held pipe and EOF alone
// makes it exit, so a carrier cannot outlive a shell that dies without killing
// it. Terminate's kill is only an accelerator.
func TestWindowsJobCarrierExitsOnStdinEOF(t *testing.T) {
	cp, err := winJobCarrier{}.StartCarrier(context.Background())
	if err != nil {
		t.Fatalf("StartCarrier: %v", err)
	}
	p := cp.(*winCarrierProc)
	if err := p.stdin.Close(); err != nil {
		t.Fatalf("closing carrier stdin: %v", err)
	}
	if sig := waitCarrier(t, cp); sig != 0 {
		t.Fatalf("Wait after stdin EOF = %d, want 0", sig)
	}
}

// TestWindowsJobCarrierTerminateIsIdempotent pins the CarrierProcess contract
// the runner leans on: reapCarrier may Terminate a carrier that an external
// kill already reaped, and that must not panic or block.
func TestWindowsJobCarrierTerminateIsIdempotent(t *testing.T) {
	cp, err := winJobCarrier{}.StartCarrier(context.Background())
	if err != nil {
		t.Fatalf("StartCarrier: %v", err)
	}
	cp.Terminate()
	waitCarrier(t, cp)
	cp.Terminate()
	cp.Terminate()
}

// TestWindowsPosixBackgroundJobRunsWithCarrier is the regression this platform
// carrier exists for: before it, POSIX mode on Windows had no OS-backed
// carrier, so WithJobCarrier's strict contract failed every `cmd &` closed
// with "background jobs need kernel-visible PIDs in POSIX mode, unsupported on
// windows" — which is what the bash 5.3 jobs and posixexp fixtures saw instead
// of their output.
func TestWindowsPosixBackgroundJobRunsWithCarrier(t *testing.T) {
	withStrictPosixEnv(t, "sh", true)
	if newCLIJobCarrier() == nil {
		t.Fatal("platformJobCarrier is nil on windows; POSIX mode would fail closed")
	}
	r, err := newRunner()
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if err := interp.StdIO(nil, &out, &errOut)(r); err != nil {
		t.Fatal(err)
	}
	file, err := syntax.NewParser().Parse(strings.NewReader(`
{ echo ran; } &
bang=$!
wait
case $bang in ''|*[!0-9]*) echo "bad-bang=$bang" ;; *) echo bang-is-a-pid ;; esac
`), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), file); err != nil {
		t.Fatalf("Run: %v\n%s", err, errOut.String())
	}
	if strings.Contains(errOut.String(), "job carrier") {
		t.Fatalf("background job refused by the carrier:\n%s", errOut.String())
	}
	for _, want := range []string{"ran", "bang-is-a-pid"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in output:\n%s%s", want, out.String(), errOut.String())
		}
	}
}

// waitCarrier calls Wait once, failing the test rather than hanging the suite
// if the carrier never exits.
func waitCarrier(t *testing.T, cp interp.CarrierProcess) int {
	t.Helper()
	done := make(chan int, 1)
	go func() { done <- cp.Wait() }()
	select {
	case sig := <-done:
		return sig
	case <-time.After(30 * time.Second):
		cp.Terminate()
		t.Fatal("carrier Wait did not return within 30s")
		return 0
	}
}

// carrierPidAlive is interp's probeProcess, the `kill -0` path, as a bool.
func carrierPidAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActiveExitCode
}
