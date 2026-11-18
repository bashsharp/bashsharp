// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
)

// Sprint 246, story 691 (`type -p` with no PATH on Windows): type5.sub does
// `touch e; chmod +x e`, then `PATH= ; type -p e` and expects "./e". Two
// Windows-only defects hid it: the empty PATH element built its candidate
// with filepath.Join, which cleans the "./" away, and the PATH lookup never
// tried the extensionless file at all, so nothing was found to print.
func TestLookPathEmptyElementKeepsDotSlash(t *testing.T) {
	t.Parallel()
	for _, windows := range []bool{false, true} {
		var tried []string
		find := func(dir, file string, exts []string) (string, error) {
			tried = append(tried, file)
			return file, nil
		}
		env := expand.ListEnviron("PATH=", "PATHEXT=.COM;.EXE")
		got, err := lookPathDirMode("/cwd", env, "e", find, windows)
		if err != nil {
			t.Fatalf("windows=%v: %v", windows, err)
		}
		if got != "./e" {
			t.Errorf("windows=%v: lookPathDirMode = %q, want %q", windows, got, "./e")
		}
		if len(tried) != 1 || tried[0] != "./e" {
			t.Errorf("windows=%v: candidates = %q, want [./e]", windows, tried)
		}
	}
}

// An unset PATH takes the same path through the lookup as an empty one.
func TestLookPathUnsetPathKeepsDotSlash(t *testing.T) {
	t.Parallel()
	find := func(dir, file string, exts []string) (string, error) { return file, nil }
	got, err := lookPathDirMode("/cwd", expand.ListEnviron("PATHEXT=.COM;.EXE"), "e", find, true)
	if err != nil {
		t.Fatal(err)
	}
	if got != "./e" {
		t.Errorf("lookPathDirMode = %q, want %q", got, "./e")
	}
}

// findExecutable's Windows branch (a non-empty PATHEXT list) used to try
// only name+ext, so an extensionless executable was never found.
func TestFindExecutableExtensionless(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "e"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	exts := []string{".com", ".exe", ".bat", ".cmd"}
	got, err := findExecutable(dir, "e", exts)
	if err != nil {
		t.Fatalf("findExecutable(e): %v", err)
	}
	if got != "e" {
		t.Errorf("findExecutable(e) = %q, want %q", got, "e")
	}
	if _, err := findExecutable(dir, "missing", exts); err == nil {
		t.Error("findExecutable found a file that does not exist")
	}
	// A directory is still not an executable.
	if err := os.Mkdir(filepath.Join(dir, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := findExecutable(dir, "d", exts); err == nil {
		t.Error("findExecutable accepted a directory")
	}
	// PATHEXT keeps precedence: foo.exe beside a plain foo wins.
	if err := os.WriteFile(filepath.Join(dir, "both"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "both.exe"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := findExecutable(dir, "both", exts); err != nil || got != "both" {
		t.Errorf("findExecutable(both) = %q, %v", got, err)
	}
}
