//go:build unix

package interp

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Zed captures a login shell's environment with `... >&0` while fd 0 is the
// write end it reads back. Bash accepts `>&0` when the inherited fd 0 is open
// for output and refuses it (EBADF) when fd 0 is read-only.
func TestDupOutToWritableStdin(t *testing.T) {
	run := func(t *testing.T, stdin *os.File, src string) (string, error) {
		t.Helper()
		file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(src), "./s")
		if err != nil {
			t.Fatal(err)
		}
		var errBuf bytes.Buffer
		r, err := New(Dir(t.TempDir()), StdIO(stdin, &bytes.Buffer{}, &errBuf))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err = r.Run(ctx, file)
		return errBuf.String(), err
	}

	path := filepath.Join(t.TempDir(), "fd0")
	w, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if stderr, err := run(t, w, "echo via-fd0 >&0"); err != nil {
		t.Fatalf("echo >&0 on a write-only stdin failed: %v %s", err, stderr)
	}
	w.Close()
	if got, _ := os.ReadFile(path); string(got) != "via-fd0\n" {
		t.Fatalf("fd 0 received %q, want %q", got, "via-fd0\n")
	}

	ro, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	stderr, err := run(t, ro, "echo nope >&0")
	if err == nil || !strings.Contains(stderr, "Bad file descriptor") {
		t.Fatalf("echo >&0 on a read-only stdin: err=%v stderr=%q, want EBADF", err, stderr)
	}
}
