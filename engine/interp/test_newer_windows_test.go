//go:build windows

package interp

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestWindowsTestNewerUsesNativeAccessTime(t *testing.T) {
	dir := t.TempDir()
	path := dir + `\newer`
	if err := os.WriteFile(path, []byte("body"), 0o666); err != nil {
		t.Fatal(err)
	}

	// Reproduce the Bash corpus: establish an equally old atime and mtime,
	// then modify the file without reading it. With the modern NTFS access-time
	// policy, the write advances LastWriteTime and leaves LastAccessTime at the
	// last value the filesystem recorded.
	baseline := time.Date(2024, time.January, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(path, baseline, baseline); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed"), 0o666); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	if !ok {
		t.Fatalf("os.Stat Sys = %T, want *syscall.Win32FileAttributeData", info.Sys())
	}
	accessed := time.Unix(0, data.LastAccessTime.Nanoseconds())
	written := time.Unix(0, data.LastWriteTime.Nanoseconds())
	if !written.After(accessed) {
		t.Fatalf("os.Stat after write: LastWriteTime = %v, LastAccessTime = %v; want write after access", written, accessed)
	}
	if !modifiedSinceAccessed(info) {
		t.Fatal("modifiedSinceAccessed = false with mtime after atime")
	}

	file, err := syntax.NewParser().Parse(strings.NewReader("test -N newer"), "")
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	runner, err := New(Dir(dir), StdIO(nil, nil, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), file); err != nil {
		t.Fatalf("test -N newer = %v, stderr = %q; want success", err, stderr.String())
	}

	equal := baseline.Add(time.Hour)
	if err := os.Chtimes(path, equal, equal); err != nil {
		t.Fatal(err)
	}
	runner, err = New(Dir(dir), StdIO(nil, nil, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	err = runner.Run(context.Background(), file)
	var status ExitStatus
	if !errors.As(err, &status) || status != 1 {
		t.Fatalf("test -N newer with equal times = %v, stderr = %q; want exit status 1", err, stderr.String())
	}
}
