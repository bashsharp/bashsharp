// Copyright (c) 2017, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

//go:build linux || darwin

package interp_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"golang.org/x/sys/unix"
)

func TestRetainedCwdReadDirErrorIsPathError(t *testing.T) {
	dir := t.TempDir()
	var gotErr error
	defaultReadDir := interp.DefaultReadDirHandler2()
	runner, err := interp.New(
		interp.Dir(dir),
		interp.ReadDirHandler2(func(ctx context.Context, path string) ([]os.DirEntry, error) {
			_, gotErr = defaultReadDir(ctx, filepath.Join(path, "missing"))
			return nil, nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	file, err := syntax.NewParser().Parse(strings.NewReader(`printf '%s\n' *`), "cwd-readdir-error")
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), file); err != nil {
		t.Fatal(err)
	}
	var pathErr *os.PathError
	if !errors.As(gotErr, &pathErr) {
		t.Fatalf("read-dir error = %T %v, want *os.PathError", gotErr, gotErr)
	}
	if pathErr.Op != "open" || pathErr.Path != "missing" || !errors.Is(pathErr, os.ErrNotExist) {
		t.Fatalf("read-dir PathError = %#v, want open missing with ErrNotExist", pathErr)
	}
}

func TestRunnerGlobUsesRetainedCwdHandle(t *testing.T) {
	root := t.TempDir()
	working := root + "/working"
	moved := root + "/moved"
	if err := os.Mkdir(working, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(working+"/marker", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	runner, err := interp.New(interp.Dir(working), interp.StdIO(nil, &stdout, &stdout))
	if err != nil {
		t.Fatal(err)
	}
	prime, err := syntax.NewParser().Parse(strings.NewReader(`cd .`), "cwd-prime")
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), prime); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(working, moved); err != nil {
		t.Fatal(err)
	}
	file, err := syntax.NewParser().Parse(strings.NewReader(`printf '<%s>\n' *`), "cwd-glob")
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), file); err != nil {
		t.Fatalf("glob through retained cwd: %v; output=%q", err, stdout.String())
	}
	if got, want := stdout.String(), "<marker>\n"; got != want {
		t.Fatalf("glob output = %q, want %q", got, want)
	}
}

func TestRunnerGlobRequiresSearchPermissionAfterCd(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory search permission")
	}
	root := t.TempDir()
	working := filepath.Join(root, "working")
	if err := os.Mkdir(working, 0o755); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	runner, err := interp.New(interp.Dir(root), interp.StdIO(nil, &stdout, &stdout))
	if err != nil {
		t.Fatal(err)
	}
	file, err := syntax.NewParser().Parse(strings.NewReader(`
cd working
mkdir -m a=x searchable
mkdir -m a=r readable
printf '<%s>\n' */.
`), "glob-search-permission")
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), file); err != nil {
		t.Fatal(err)
	}
	if got, want := stdout.String(), "<searchable/.>\n"; got != want {
		t.Fatalf("glob output = %q, want %q", got, want)
	}
}

func TestRunnerSubshellsDoNotLeakCwdHandles(t *testing.T) {
	fdDir := "/proc/self/fd"
	if runtime.GOOS == "darwin" {
		fdDir = "/dev/fd"
	}
	countFDs := func() int {
		if runtime.GOOS == "darwin" {
			var limit unix.Rlimit
			if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &limit); err != nil {
				t.Fatal(err)
			}
			maxFD := min(int(limit.Cur), 4096)
			count := 0
			for fd := range maxFD {
				if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err == nil {
					count++
				}
			}
			return count
		}
		entries, err := os.ReadDir(fdDir)
		if err != nil {
			t.Fatal(err)
		}
		return len(entries)
	}

	file, err := syntax.NewParser().Parse(strings.NewReader(`
i=0
while test "$i" -lt 256; do
	(:)
	value=$(:)
	i=$((i + 1))
done
`), "cwd-fd-leak")
	if err != nil {
		t.Fatal(err)
	}
	runner, err := interp.New(interp.Dir(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}

	oldGCPercent := debug.SetGCPercent(-1)
	defer debug.SetGCPercent(oldGCPercent)
	before := countFDs()
	if err := runner.Run(context.Background(), file); err != nil {
		t.Fatal(err)
	}
	for range 256 {
		runner.Reset()
	}
	after := countFDs()
	if delta := after - before; delta > 4 {
		t.Fatalf("subshells leaked %d cwd descriptors", delta)
	}
}
