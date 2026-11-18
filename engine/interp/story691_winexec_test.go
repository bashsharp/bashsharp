// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp

import (
	"os"
	"path/filepath"
	"testing"
)

// Sprint 246, story 691 (EACCES on Windows): exec3.sub writes `echo bar`
// into x.sh with a plain redirect and expects `exec ./x.sh` to be refused
// — on Unix by the missing 0111 bits. Windows has no execute bit, and the
// lookup skipped the check entirely, so the script ran: it sourced
// $BASH_ENV again and execscript saw a second "this is bashenv" where the
// trap dump belonged.
func TestWindowsExecutableFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	exts := []string{".com", ".exe", ".bat", ".cmd"}

	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}

	tests := []struct {
		name, content string
		want          bool
	}{
		// The regression: a data file with an extension Windows does not run.
		{"x.sh", "echo bar\n", false},
		{"x.output", "some text\n", false},
		// A script that says what runs it is a program on any platform.
		{"shebang.sh", "#!/bin/sh\necho bar\n", true},
		// So is an image, whatever it is called.
		{"prog.dat", "MZ\x90\x00", true},
		{"elf.dat", "\x7fELF\x02", true},
		// PATHEXT is what Windows itself runs.
		{"tool.exe", "", true},
		{"TOOL2.EXE", "", true},
		{"script.cmd", "echo bar\n", true},
		// A name with no extension is how a POSIX tool is spelled; type5.sub
		// does `touch e; chmod +x e` and wants `type -p e` to find it.
		{"e", "", true},
		{"noext", "some text\n", true},
	}
	for _, tc := range tests {
		path := write(tc.name, tc.content)
		if got := windowsExecutableFile(path, exts); got != tc.want {
			t.Errorf("windowsExecutableFile(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}

	// A dot in a parent directory is not the file's extension.
	sub := filepath.Join(dir, "a.b")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(sub, "prog")
	if err := os.WriteFile(inner, []byte("text\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !windowsExecutableFile(inner, exts) {
		t.Errorf("windowsExecutableFile(%q) = false, want true", inner)
	}
}

// The PATH lookup must report a refused file as a permission error, not as
// "not found": bash's exec of an unrunnable file is EACCES (126), while a
// missing one is 127.
func TestFindExecutableRefusesNonExecutable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x.sh"), []byte("echo bar\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	exts := []string{".com", ".exe", ".bat", ".cmd"}
	_, err := findExecutable(dir, "x.sh", exts)
	if err == nil {
		t.Fatal("findExecutable ran a file with no shebang and no runnable extension")
	}
	if err.Error() != "permission denied" {
		t.Errorf("findExecutable(x.sh) error = %q, want %q", err, "permission denied")
	}
	// The same file with a shebang is a script the shell may run.
	if err := os.WriteFile(filepath.Join(dir, "ok.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := findExecutable(dir, "ok.sh", exts); err != nil || got != "ok.sh" {
		t.Errorf("findExecutable(ok.sh) = %q, %v", got, err)
	}
}
