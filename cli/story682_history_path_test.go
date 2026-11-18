// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Sprint 245, story 682: $HISTFILE and the startup files are spelled by the
// shell (/tmp/…, /c/Users/… on Windows), so every open of one goes through
// interp.ShellPathToOS. history.tests sets HISTFILE under /tmp, loads it at
// startup and then searches it with C-r for "(left"; a drive-relative
// \tmp\… open found nothing and the search came back empty.

func TestStory682HistoryFileRoundTrip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, ".bash_history")
	entries := []string{"echo one", "echo two"}
	writeSessionHistory(dir, path, entries, false, "__unset__")

	e := &rlEmu{base: 1, max: -1, dir: dir}
	e.loadFile(path)
	if len(e.hist) != len(entries) {
		t.Fatalf("loaded %q, want %q", e.hist, entries)
	}
	for i := range entries {
		if e.hist[i] != entries[i] {
			t.Errorf("entry %d = %q, want %q", i, e.hist[i], entries[i])
		}
	}
}

// An empty HISTFILE is not a path: it must stay empty rather than becoming
// the cwd.
func TestStory682ShellFilePathEmpty(t *testing.T) {
	t.Parallel()

	if got := shellFilePath("/some/dir", ""); got != "" {
		t.Errorf("shellFilePath(empty) = %q, want empty", got)
	}
}

// Off Windows the shell's spelling is the host's, so the conversion is the
// identity and nothing about the existing behaviour moves.
func TestStory682ShellFilePathIsIdentityOffWindows(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("the conversion is only a no-op off Windows")
	}
	for _, p := range []string{"/tmp/x/.bash_history", "rel/hist", "/c/Users/me/.bash_history"} {
		if got := shellFilePath("/work", p); got != p {
			t.Errorf("shellFilePath(%q) = %q, want it unchanged", p, got)
		}
	}
}

// The login-shell startup files are looked for through the same conversion,
// so `HOME=$TDIR bash --login` finds ~/.bash_profile and ~/.bash_logout
// (invocation.tests) wherever $HOME is spelled.
func TestStory682StartupFileExists(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	const dir = "/"
	path := shellHomePath(home, ".bash_profile")
	if startupFileExists(dir, path) {
		t.Fatalf("%s exists before it was written", path)
	}
	if err := os.WriteFile(filepath.Join(home, ".bash_profile"), []byte("echo hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !startupFileExists(dir, path) {
		t.Errorf("%s not found after it was written", path)
	}
	for _, name := range []string{".bash_logout", ".bashrc", ".bashyrc"} {
		if startupFileExists(dir, shellHomePath(home, name)) {
			t.Errorf("%s reported present", name)
		}
	}
}
