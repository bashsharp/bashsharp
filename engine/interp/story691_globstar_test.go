// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Sprint 246, story 691 (globstar and symlinks): globstar3.sub lays out a/
// b/ and `ln -s a c`, then expects `echo **` to stop at c. The globber's
// symlink probe took the shell's spelling of the path straight to
// os.Lstat, which on Windows cannot open /tmp/x or /c/Users/x; the error
// read as "not a symlink" and the walk descended. It now goes through the
// runner's own stat handler, so the path is converted like every other one.
func TestGlobStarSymlinkUsesStatHandler(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"aa", "ab"} {
		if err := os.WriteFile(filepath.Join(dir, "a", f), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("a", filepath.Join(dir, "c")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	var stdout bytes.Buffer
	var lstatted []string
	r, err := New(
		Dir(dir),
		StdIO(nil, &stdout, &stdout),
		StatHandler(func(ctx context.Context, name string, follow bool) (fs.FileInfo, error) {
			if !follow {
				lstatted = append(lstatted, name)
			}
			return DefaultStatHandler()(ctx, name, follow)
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	file, err := syntax.NewParser().Parse(bytes.NewReader([]byte("shopt -s globstar; echo **\n")), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), file); err != nil {
		t.Fatal(err)
	}
	if got, want := stdout.String(), "a a/aa a/ab c\n"; got != want {
		t.Errorf("echo ** printed %q, want %q", got, want)
	}
	// The probe must reach the stat handler, which is what converts the
	// shell's spelling into a path the host can open.
	found := false
	for _, p := range lstatted {
		if filepath.Base(p) == "c" {
			found = true
		}
	}
	if !found {
		if len(lstatted) == 0 {
			t.Error("the globstar walk never lstat'ed anything through the handler")
		} else {
			t.Errorf("the globstar walk never lstat'ed the symlink; saw %q", lstatted)
		}
	}
}
