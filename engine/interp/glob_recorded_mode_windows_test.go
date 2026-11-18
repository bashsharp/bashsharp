//go:build windows

package interp

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/qiangli/bash/winmode"
)

// A directory with execute but no read permission can be traversed by an
// explicit name, but its entries cannot be enumerated by a glob. Windows
// accepts os.ReadDir even after chmod records that POSIX mode in the ACL.
func TestGlobHonorsRecordedDirectoryReadModeOnWindows(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(a, "b")
	if err := os.MkdirAll(b, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b, "c"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := winmode.Set(a, 0o300); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = winmode.Set(a, 0o700) })
	var out bytes.Buffer
	r, err := New(Dir(dir), StdIO(nil, &out, &out))
	if err != nil {
		t.Fatal(err)
	}
	file, err := syntax.NewParser().Parse(strings.NewReader("printf '<%s>\\n' ./a/* ./a/b/*\n"), "glob-test")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), file); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "<./a/*>\n<./a/b/c>\n"; got != want {
		t.Fatalf("glob after chmod -r: got %q, want %q", got, want)
	}
}
