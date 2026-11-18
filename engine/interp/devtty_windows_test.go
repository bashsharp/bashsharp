//go:build windows

package interp

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// /dev/tty on Windows: always a character device to stat, the console to open.
func TestDevTTYWindowsMapping(t *testing.T) {
	info, ok := devTTYStat("/dev/tty")
	if !ok || info.Mode()&fs.ModeCharDevice == 0 {
		t.Fatalf("stat /dev/tty = %v, %v", info, ok)
	}
	if _, ok := devTTYStat("/dev/ttyS0"); ok {
		t.Fatal("only /dev/tty itself is synthesized")
	}
	if p, ok := devTTYConsolePath("/dev/tty", os.O_RDONLY); !ok || p != `CONIN$` {
		t.Fatalf("read open = %q, %v", p, ok)
	}
	if p, _ := devTTYConsolePath("/dev/tty", os.O_WRONLY|os.O_APPEND); p != `CONOUT$` {
		t.Fatalf("write open = %q", p)
	}
	if _, ok := devTTYConsolePath("/dev/null", os.O_RDONLY); ok {
		t.Fatal("/dev/null is not the console")
	}
	if got := userGroups(); len(got) == 0 || got[0] == "-1" {
		t.Fatalf("GROUPS on Windows = %v; want at least one non-negative group", got)
	}
}

// The test builtin reaches the synthetic /dev/tty through Runner.stat and
// Runner.access, which absolutize their operand first: the POSIX spelling
// must survive that step for -c/-e/-r/-w to see the character device.
func TestDevTTYWindowsTestBuiltin(t *testing.T) {
	if got := absPath(`C:\work`, "/dev/tty"); got != "/dev/tty" {
		t.Fatalf("absPath(/dev/tty) = %q, want the spelling kept", got)
	}
	if got := absPath(`C:\work`, "/dev/ttyS0"); got == "/dev/ttyS0" {
		t.Fatalf("absPath(/dev/ttyS0) = %q, want a native path", got)
	}
	src := `
test -c /dev/tty && echo c
test -e /dev/tty && echo e
test -r /dev/tty && echo r
test -w /dev/tty && echo w
test -f /dev/tty || echo nf
test -d /dev/tty || echo nd
test -x /dev/tty || echo nx
[[ -c /dev/tty ]] && echo cc
[ -c /dev/tty ] && echo cb
cd /dev/tty 2>/dev/null || echo ncd
`
	file, err := syntax.NewParser().Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	r, err := New(Dir(t.TempDir()), StdIO(nil, &out, &errb))
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Run(context.Background(), file)
	want := "c\ne\nr\nw\nnf\nnd\nnx\ncc\ncb\nncd\n"
	if out.String() != want {
		t.Fatalf("stdout = %q, stderr = %q; want %q", out.String(), errb.String(), want)
	}
}
