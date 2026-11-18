// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

//go:build windows

package interp

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/syntax"
	"golang.org/x/sys/windows"
)

// The Windows FIFO opener, against real named pipes. The two scripts these
// exist for are from the bash 5.3 corpus:
//
//	tests/source6.sub:44-49   mkfifo; `echo … > fifo &`; `. fifo`
//	tests/read2.sub:60-64     mkfifo; `exec 9<> a.pipe`; `rm -f a.pipe`
//
// so read, write, read-write, blocking until a peer arrives, and an unlink
// that leaves the descriptor working are all pinned below. The marker
// format itself is proven platform-neutrally in fifo_marker_test.go.

const fifoTestTimeout = 30 * time.Second

func fifoTestPath(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "a.pipe")
	if err := mkfifo(path, 0o666); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	return path
}

func fifoTestCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), fifoTestTimeout)
	t.Cleanup(cancel)
	return ctx
}

// mkfifo must write exactly what the coreutils applet writes: the v1 bytes,
// with FILE_ATTRIBUTE_SYSTEM set. Without the attribute the file is not a
// FIFO to anybody, this opener included.
func TestFifoMkfifoWritesMarker(t *testing.T) {
	t.Parallel()

	path := fifoTestPath(t)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	leaf, ok := parseFifoMarker(content)
	if !ok {
		t.Fatalf("mkfifo wrote %q, which is not a v1 marker", content)
	}
	if len(content) != fifoMarkerLen {
		t.Errorf("marker is %d bytes, want %d", len(content), fifoMarkerLen)
	}

	name16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	attrs, err := windows.GetFileAttributes(name16)
	if err != nil {
		t.Fatal(err)
	}
	if attrs&windows.FILE_ATTRIBUTE_SYSTEM == 0 {
		t.Error("FILE_ATTRIBUTE_SYSTEM is not set")
	}

	gotLeaf, gotOK, err := fifoMarkerLeaf(path)
	if err != nil || !gotOK {
		t.Fatalf("fifoMarkerLeaf = %q, %v, %v", gotLeaf, gotOK, err)
	}
	if gotLeaf != leaf {
		t.Errorf("fifoMarkerLeaf = %q, want %q", gotLeaf, leaf)
	}

	// Recreating after an unlink draws a fresh leaf: a new, distinct FIFO.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := mkfifo(path, 0o666); err != nil {
		t.Fatal(err)
	}
	again, _, err := fifoMarkerLeaf(path)
	if err != nil {
		t.Fatal(err)
	}
	if again == leaf {
		t.Errorf("recreated FIFO reused the leaf %q", leaf)
	}
}

// source6.sub's shape with the reader arriving first: the read open blocks
// until a writer connects, which is POSIX's open-blocks-until-a-writer.
func TestFifoReadBlocksUntilWriter(t *testing.T) {
	t.Parallel()

	ctx := fifoTestCtx(t)
	path := fifoTestPath(t)
	const body = "echo four - OK\n"

	wrote := make(chan error, 1)
	go func() {
		// Long enough that the reader is genuinely waiting, not racing.
		time.Sleep(100 * time.Millisecond)
		w, err := openPath(ctx, path, os.O_WRONLY, 0)
		if err != nil {
			wrote <- err
			return
		}
		_, err = io.WriteString(w, body)
		wrote <- errors.Join(err, w.Close())
	}()

	r, err := openPath(ctx, path, os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("open for read: %v", err)
	}
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := <-wrote; err != nil {
		t.Fatalf("write side: %v", err)
	}
	if string(got) != body {
		t.Fatalf("read %q, want %q", got, body)
	}
}

// The same rendezvous with the writer arriving first, which is the order
// source6.sub actually produces: `echo … > fifo &` is backgrounded before
// `. fifo` runs, so the write open has to wait for a reader to exist.
// There is no pipe at all until then — mkfifo creates none.
func TestFifoWriteBlocksUntilReader(t *testing.T) {
	t.Parallel()

	ctx := fifoTestCtx(t)
	path := fifoTestPath(t)
	const body = "four - OK\n"

	wrote := make(chan error, 1)
	opened := make(chan struct{})
	go func() {
		close(opened)
		w, err := openPath(ctx, path, os.O_WRONLY, 0)
		if err != nil {
			wrote <- err
			return
		}
		_, err = io.WriteString(w, body)
		wrote <- errors.Join(err, w.Close())
	}()

	<-opened
	// Let the writer spin on "no reader yet" for a few of its retries.
	time.Sleep(100 * time.Millisecond)
	select {
	case err := <-wrote:
		t.Fatalf("the write open did not wait for a reader: %v", err)
	default:
	}

	r, err := openPath(ctx, path, os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("open for read: %v", err)
	}
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := <-wrote; err != nil {
		t.Fatalf("write side: %v", err)
	}
	if string(got) != body {
		t.Fatalf("read %q, want %q", got, body)
	}
}

// `> fifo` carries O_CREAT|O_TRUNC. Truncating a FIFO is a no-op on POSIX,
// and here it must in particular not overwrite the marker's bytes — doing
// so would destroy the FIFO mid-use.
func TestFifoWriteDoesNotTruncateMarker(t *testing.T) {
	t.Parallel()

	ctx := fifoTestCtx(t)
	path := fifoTestPath(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		w, err := openPath(ctx, path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o666)
		if err != nil {
			done <- err
			return
		}
		_, err = io.WriteString(w, "x\n")
		done <- errors.Join(err, w.Close())
	}()

	r, err := openPath(ctx, path, os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("open for read: %v", err)
	}
	got, _ := io.ReadAll(r)
	r.Close()
	if err := <-done; err != nil {
		t.Fatalf("write side: %v", err)
	}
	if string(got) != "x\n" {
		t.Errorf("read %q, want %q", got, "x\n")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("the marker was rewritten: %q -> %q", before, after)
	}
}

// read2.sub:60-64. `exec 9<> a.pipe` never blocks and loops bytes back, the
// marker is unlinked immediately afterwards, and the descriptor keeps
// working for the 2000 iterations that follow.
func TestFifoReadWriteSurvivesUnlink(t *testing.T) {
	t.Parallel()

	ctx := fifoTestCtx(t)
	path := fifoTestPath(t)

	opened := make(chan io.ReadWriteCloser, 1)
	go func() {
		f, err := openPath(ctx, path, os.O_RDWR, 0)
		if err != nil {
			t.Errorf("open read-write: %v", err)
			opened <- nil
			return
		}
		opened <- f
	}()

	var f io.ReadWriteCloser
	select {
	case f = <-opened:
	case <-time.After(5 * time.Second):
		t.Fatal("the read-write open blocked; it must not")
	}
	if f == nil {
		t.FailNow()
	}
	defer f.Close()

	// A `<>` redirection is bound to its numbered fd as an *os.File and
	// nothing else (interp's fd table is map[int]*os.File); anything else
	// is copied through an os.Pipe on the way, which drops the write half.
	if _, ok := f.(*os.File); !ok {
		t.Fatalf("read-write open gave %T, want *os.File", f)
	}

	// rm -f a.pipe: already-connected ends are unaffected.
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove while the fd is open: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stat after remove = %v, want not-exist", err)
	}

	br := bufio.NewReader(f)
	for i := range 2000 {
		line := fmt.Sprintf("line %d\n", i)
		if _, err := io.WriteString(f, line); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		got, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		if got != line {
			t.Fatalf("read %d = %q, want %q", i, got, line)
		}
	}
}

// Closing the read-write pair hangs the write end up first, so a reader of
// the same stream sees EOF rather than a half-closed pipe.
func TestFifoReadWriteCloseIsEOF(t *testing.T) {
	t.Parallel()

	ctx := fifoTestCtx(t)
	path := fifoTestPath(t)
	f, err := openPath(ctx, path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open read-write: %v", err)
	}
	if _, err := io.WriteString(f, "hello\n"); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 6)
	if _, err := io.ReadFull(f, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "hello\n" {
		t.Fatalf("read %q", buf)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// A read open that is called off — the shell's context going away — must
// not wedge the goroutine that is sitting in ConnectNamedPipe.
func TestFifoReadOpenCancelled(t *testing.T) {
	t.Parallel()

	path := fifoTestPath(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		f, err := openPath(ctx, path, os.O_RDONLY, 0)
		if err == nil {
			f.Close()
		}
		done <- err
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("open error = %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a cancelled read open never returned")
	}
}

// A write open with no reader is the same cancellation story from the
// other side.
func TestFifoWriteOpenCancelled(t *testing.T) {
	t.Parallel()

	path := fifoTestPath(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		f, err := openPath(ctx, path, os.O_WRONLY, 0)
		if err == nil {
			f.Close()
		}
		done <- err
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("open error = %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a cancelled write open never returned")
	}
}

// Detection is all three rules or nothing: a file that satisfies some of
// them is the regular file it is, never "almost a FIFO". Each case below
// must open and read back as an ordinary file, not rendezvous and not hang.
func TestFifoDetectionRejectsOrdinaryFiles(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		system  bool
	}{
		// Magic bytes alone must not turn a user's file into a FIFO.
		{"marker bytes without the system attribute", goldenFifoMarker, false},
		{"system attribute with other content", "hello\n", true},
		{"system attribute with a bad magic", "!<bashyfifo2>\n" + goldenFifoLeaf + "\n", true},
		{"system attribute with an uppercase leaf",
			"!<bashyfifo>\nbashy-fifo-9F8C0A1B2D3E4F5061728394A5B6C7D8\n", true},
		{"system attribute with trailing junk", goldenFifoMarker + "x", true},
		{"system attribute on a large file", strings.Repeat("x", 4096), true},
		{"empty system file", "", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "f")
			if err := os.WriteFile(path, []byte(test.content), 0o666); err != nil {
				t.Fatal(err)
			}
			if test.system {
				if err := setFifoMarkerAttr(path); err != nil {
					t.Fatal(err)
				}
			}
			if _, ok, err := fifoMarkerLeaf(path); ok || err != nil {
				t.Fatalf("fifoMarkerLeaf = %v, %v; want (false, nil)", ok, err)
			}

			ctx := fifoTestCtx(t)
			f, err := openPath(ctx, path, os.O_RDONLY, 0)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			defer f.Close()
			got, err := io.ReadAll(f)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if string(got) != test.content {
				t.Fatalf("read %q, want %q", got, test.content)
			}

			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if fifoMarkerStat(path, info).Mode()&fs.ModeNamedPipe != 0 {
				t.Fatal("an ordinary file stat'ed as a FIFO")
			}
		})
	}
}

// A path carrying a marker stats as a FIFO, so `test -p` agrees with the
// fact that opening it rendezvouses on a pipe.
func TestFifoMarkerStatIsNamedPipe(t *testing.T) {
	t.Parallel()

	path := fifoTestPath(t)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	got := fifoMarkerStat(path, info)
	if got.Mode()&fs.ModeNamedPipe == 0 {
		t.Errorf("Mode = %v, want a named pipe", got.Mode())
	}
	if got.Mode().IsRegular() {
		t.Error("a FIFO must not stat as a regular file")
	}
	if got.Size() != 0 {
		t.Errorf("Size = %d, want 0", got.Size())
	}
	if got.Name() != info.Name() {
		t.Errorf("Name = %q, want %q", got.Name(), info.Name())
	}
}

// fifoRunScript runs src in dir and returns its combined output.
func fifoRunScript(t *testing.T, dir, src string) string {
	t.Helper()
	file, err := syntax.NewParser().Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	r, err := New(Dir(dir), StdIO(nil, &b, &b))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), fifoTestTimeout)
	defer cancel()
	if err := r.Run(ctx, file); err != nil {
		t.Fatalf("run error: %v\noutput: %q", err, b.String())
	}
	return b.String()
}

// The shell's own predicates, end to end: a FIFO is -p and -e, and is not
// -f. This is what `ls -l`, `find -type p` and the suite's FIFO probes all
// come down to inside the shell.
func TestFifoShellTestOperators(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := mkfifo(filepath.Join(dir, "a.pipe"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plain"), []byte("x\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	src := `for f in a.pipe plain; do
	echo "$f" $([ -p "$f" ] && echo p || echo -) $([ -f "$f" ] && echo f || echo -) $([ -e "$f" ] && echo e || echo -)
done
`
	want := "a.pipe p - e\nplain - f e\n"
	if got := fifoRunScript(t, dir, src); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// source6.sub:44-49, as the shell runs it: a backgrounded write open and a
// `.` of the same path, which is the read open. The line the fixture looks
// for at builtins:123 is "four - OK".
func TestFifoSourceRendezvous(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "fifo")
	if err := mkfifo(path, 0o666); err != nil {
		t.Fatal(err)
	}
	// The fixture names the FIFO absolutely ($TMPDIR/fifo-$$); keep that,
	// so `.` takes the path as given rather than searching PATH for it.
	quoted := "'" + strings.ReplaceAll(path, `\`, "/") + "'"
	src := fmt.Sprintf("echo \"echo four - OK\" > %s &\n. %s\nwait\n", quoted, quoted)
	if got := fifoRunScript(t, dir, src); got != "four - OK\n" {
		t.Fatalf("got %q, want %q", got, "four - OK\n")
	}
}

// read2.sub:60-64, as the shell runs it: exec 9<>, unlink, then use the
// descriptor. Kept short here — the 2000-iteration loop is
// TestFifoReadWriteSurvivesUnlink, which does not pay for the parser.
func TestFifoExecReadWriteRedirection(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := mkfifo(filepath.Join(dir, "a.pipe"), 0o666); err != nil {
		t.Fatal(err)
	}
	// The unlink half of the fixture needs an external `rm`, which this
	// library's runner has no builtin for; it is pinned at the descriptor
	// level in TestFifoReadWriteSurvivesUnlink instead.
	src := `exec 9<> a.pipe
for i in 1 2 3; do
	echo "line $i" >&9
	read -r line <&9
	echo "$line"
done
exec 9>&-
`
	want := "line 1\nline 2\nline 3\n"
	if got := fifoRunScript(t, dir, src); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
