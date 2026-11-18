// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package expand

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

// Sprint 246, story 691 (globstar and symlinks on Windows): globstar3.sub
// lays out a/ b/ and `ln -s a c`, then expects `echo **` to stop at c —
// bash includes the symlink but does not descend it. The walk asked
// [os.Lstat] directly, with the shell's own spelling of the path; on
// Windows /tmp/x and /c/Users/x are not paths os.Lstat can open, the error
// read as "not a symlink", and `**` printed c/aa and c/ab after c. The
// lookup now goes through [Config.Lstat], the seam the interpreter fills
// with its own stat handler.
func TestGlobStarDoesNotDescendSymlink(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, sub := range []string{"a", "b"} {
		if err := os.Mkdir(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"a/aa", "a/ab", "b/bb", "b/bc"} {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(f)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("a", filepath.Join(dir, "c")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	var seen []string
	cfg := prepareConfig(&Config{
		Env:      ListEnviron("PWD=" + dir),
		GlobStar: true,
		ReadDir2: func(p string) ([]fs.DirEntry, error) { return os.ReadDir(p) },
		Lstat: func(p string) (fs.FileInfo, error) {
			seen = append(seen, p)
			return os.Lstat(p)
		},
	})

	got, err := cfg.glob(dir, "**")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a", "a/aa", "a/ab", "b", "b/bb", "b/bc", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("glob(**) =\n %q\nwant\n %q", got, want)
	}
	if !slices.Contains(seen, filepath.Join(dir, "c")) {
		t.Errorf("the walk never asked Config.Lstat about the symlink; asked %q", seen)
	}

	// A pattern that descends past `**` drops the symlink entirely.
	got, err = cfg.glob(dir, "**/*b")
	if err != nil {
		t.Fatal(err)
	}
	want = []string{"a/ab", "b", "b/bb"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("glob(**/*b) =\n %q\nwant\n %q", got, want)
	}
}

// Without a Lstat seam the walk still falls back to os.Lstat, so library
// callers that never set one keep the behaviour they had.
func TestGlobStarSymlinkFallsBackToOsLstat(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a", "aa"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a", filepath.Join(dir, "c")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	cfg := prepareConfig(&Config{
		Env:      ListEnviron("PWD=" + dir),
		GlobStar: true,
		ReadDir2: func(p string) ([]fs.DirEntry, error) { return os.ReadDir(p) },
	})
	got, err := cfg.glob(dir, "**")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a", "a/aa", "c"}; !reflect.DeepEqual(got, want) {
		t.Errorf("glob(**) = %q, want %q", got, want)
	}
}
