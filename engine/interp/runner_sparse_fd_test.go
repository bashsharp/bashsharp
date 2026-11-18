//go:build unix

package interp

import (
	"os"
	"testing"
	"time"
)

func TestExecExtraFilesPreservesSparseNumber(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	r := &Runner{fdTable: map[int]*os.File{8: f}}
	extra, inherited, cleanup, err := r.execExtraFiles()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if got, want := len(extra), 6; got != want {
		t.Fatalf("ExtraFiles length = %d, want %d for child fds 3..8", got, want)
	}
	for fd := 3; fd < 8; fd++ {
		placeholder := extra[fd-3]
		if placeholder == nil {
			t.Fatalf("child fd %d placeholder is nil", fd)
		}
		if got := placeholder.Fd(); got != ^uintptr(0) {
			t.Fatalf("child fd %d placeholder is open as parent fd %d", fd, got)
		}
	}
	if extra[5] != f {
		t.Fatalf("child fd 8 = %p, want source file %p", extra[5], f)
	}
	if inherited != "8" {
		t.Fatalf("inherited descriptor list = %q, want %q", inherited, "8")
	}
}

func TestExecExtraFilesRejectsHugeSparseNumber(t *testing.T) {
	r := &Runner{
		fdTable:        map[int]*os.File{999999999: os.Stdin},
		ulimitOverride: map[string]string{"-n": "32"},
	}
	done := make(chan error, 1)
	go func() {
		_, _, _, err := r.execExtraFiles()
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("execExtraFiles accepted a descriptor above the open-files limit")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("execExtraFiles iterated or allocated through a huge sparse descriptor")
	}
}
