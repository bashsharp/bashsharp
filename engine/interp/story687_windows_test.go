// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

//go:build windows

package interp

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/qiangli/bash/winmode"
)

// A recorded mode decides what a redirection may open, for reads as well
// as for writes. Windows refuses the write half on its own (chmod clears
// the read-only attribute with the w bit), so before this the two halves
// of redir12.sub disagreed: `> unwritable-file` failed and `<
// unreadable-file` succeeded.
func TestStory687RecordedModeDeniesOpen(t *testing.T) {
	dir := t.TempDir()
	write := func(name string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("x"), 0o666); err != nil {
			t.Fatal(err)
		}
		return path
	}
	unrecorded := write("plain")
	for _, flag := range []int{os.O_RDONLY, os.O_WRONLY, os.O_RDWR} {
		if err := recordedModeDenies(unrecorded, flag); err != nil {
			t.Errorf("flag %d on a file nobody chmod'ed: %v", flag, err)
		}
	}
	for _, tc := range []struct {
		mode                       fs.FileMode
		read, writeOnly, readWrite bool // whether the open is denied
	}{
		{0o600, false, false, false},
		{0o400, false, true, true},
		{0o200, true, false, true},
		{0o000, true, true, true},
	} {
		path := write(tc.mode.String())
		if err := winmode.Set(path, tc.mode); err != nil {
			t.Fatalf("Set(%v): %v", tc.mode, err)
		}
		for _, sub := range []struct {
			flag       int
			wantDenied bool
		}{
			{os.O_RDONLY, tc.read},
			{os.O_WRONLY, tc.writeOnly},
			{os.O_RDWR, tc.readWrite},
		} {
			err := recordedModeDenies(path, sub.flag)
			if denied := err != nil; denied != sub.wantDenied {
				t.Errorf("mode %v flag %d: denied=%v, want %v (%v)",
					tc.mode, sub.flag, denied, sub.wantDenied, err)
			}
			if err != nil && !os.IsPermission(err) {
				t.Errorf("mode %v flag %d: %v is not a permission error", tc.mode, sub.flag, err)
			}
		}
	}
}

// A process-substitution pipe is looked up by name, not by stat'ing it.
// `. <(cmd)` resolves its operand through checkStat before opening it, and
// a CreateFile on the pipe name — which is what os.Stat does — would take
// the consumer's one connection and leave the open to fail with "all pipe
// instances are busy". No pipe is created here on purpose: the lookup must
// answer without the filesystem, so it answers without a server too.
func TestStory687ProcSubstPipeLookupDoesNotConnect(t *testing.T) {
	// No pipe is created, only registered: the lookup must answer from the
	// name alone. An unregistered name is a pipe the shell has released,
	// and checkStat reports it gone rather than reaching for it.
	procSubstPipeRegister(fifoNamePrefix + "deadbeef")
	defer procSubstPipeRelease(fifoNamePrefix + "deadbeef")
	for _, path := range []string{
		windowsProcSubstShellDir + fifoNamePrefix + "deadbeef",
		windowsProcSubstNativeDir + fifoNamePrefix + "deadbeef",
	} {
		got, err := checkStat("", path, false)
		if err != nil {
			t.Errorf("checkStat(%q): %v", path, err)
			continue
		}
		if got != path {
			t.Errorf("checkStat(%q) = %q", path, got)
		}
	}
	// An ordinary missing path is still a miss.
	if _, err := checkStat("", filepath.Join(t.TempDir(), "gone"), false); err == nil {
		t.Error("checkStat found a file that is not there")
	}
}
