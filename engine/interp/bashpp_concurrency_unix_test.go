// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

//go:build unix

package interp

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

func TestBashPPGoArmsBeforeBlockingFIFORedirect(t *testing.T) {
	path := t.TempDir() + "/handshake.fifo"
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runBashPPConcurrency(t, strings.ReplaceAll(`
func reader(ack) { /bin/cat < FIFO; ack <- ok; }
func main() {
 ack := make(chan string)
 go reader(ack)
 echo ready > FIFO
 <-ack
}
main()
`, "FIFO", path))
	if err != nil || out != "ready\n" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

// TestBashPPGoFIFORedirectWaitsForTheWriter pins the rendezvous itself,
// independently of the scheduler.
//
// TestBashPPGoArmsBeforeBlockingFIFORedirect above only caught the missing
// rendezvous at GOMAXPROCS=1; with more procs the writer usually reached the
// FIFO before the task read it, so a descriptor that could not wait still
// looked correct. Here the owner is delayed past the task's open, which is the
// ordering the FIFO exists to survive: a task that opened with O_NONBLOCK sees
// end-of-file at once, writes nothing, and closes the only read end — and the
// owner's own open then blocks forever with no reader. So the failure mode is
// a hang, and the test bounds it rather than inheriting the package timeout.
func TestBashPPGoFIFORedirectWaitsForTheWriter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rendezvous.fifo")
	sink := filepath.Join(dir, "sink")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	src := `
func reader(ack) { /bin/cat < ` + path + ` > ` + sink + `; ack <- ok; }
func main() {
 ack := make(chan string)
 go reader(ack)
 /bin/sleep 0.2
 echo ready > ` + path + `
 <-ack
}
main()
`
	type result struct {
		out string
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := runBashPPConcurrency(t, src)
		done <- result{out, err}
	}()
	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("out=%q err=%v", res.out, res.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("FIFO redirect never joined the writer: the task took a descriptor that reads EOF, then closed the only read end")
	}
	got, err := os.ReadFile(sink)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "ready\n" {
		t.Fatalf("sink = %q, want %q", got, "ready\n")
	}
}

func TestBashPPTaskReadArmsAfterAvailablePrefix(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	defer write.Close()
	if _, err := write.WriteString("pre"); err != nil {
		t.Fatal(err)
	}
	armed := make(chan struct{})
	reader := taskReadReader(context.Background(), read, time.Time{}, nil, func() bool {
		select {
		case <-armed:
		default:
			close(armed)
		}
		return true
	})
	if reader == nil {
		t.Fatal("os.Pipe read descriptor was not nonblocking")
	}
	type result struct {
		line string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		line, err := bufio.NewReader(reader).ReadString('\n')
		done <- result{line, err}
	}()
	select {
	case <-armed:
	case <-time.After(time.Second):
		t.Fatal("reader did not arm after draining the available prefix")
	}
	if _, err := write.WriteString("fix\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.err != nil || got.line != "prefix\n" {
			t.Fatalf("partial read result=%q err=%v", got.line, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("partial-line reader did not finish")
	}
}

func TestBashPPTaskSnapshotPipeReadFailsClosed(t *testing.T) {
	stdinRead, stdinWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdinRead.Close()
	defer stdinWrite.Close()
	var output strings.Builder // bashpp-racegate:safe-synchronized
	r, err := New(Lang(syntax.LangBashPP), StdIO(stdinRead, &output, &output))
	if err != nil {
		t.Fatal(err)
	}
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(`
func blocked() { read value; }
func fail() { return 7; }
func main() { go blocked(); go fail(); }
main()
`), "pipe-cancel.bpp")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = r.Run(ctx, f)
	if err == nil || ctx.Err() != nil || strings.Contains(output.String(), "exit status 7") ||
		!strings.Contains(output.String(), "blocking input is unavailable") {
		t.Fatalf("out=%q err=%v ctx=%v", output.String(), err, ctx.Err())
	}
}

func TestBashPPTaskBlockingTTYReadFailsClosed(t *testing.T) {
	primary, secondary, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer primary.Close()
	defer secondary.Close()
	var output strings.Builder // bashpp-racegate:safe-synchronized
	r, err := New(Lang(syntax.LangBashPP), StdIO(secondary, &output, &output))
	if err != nil {
		t.Fatal(err)
	}
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(`
func blocked() { read value; }
func later() { echo escaped; }
func main() { go blocked(); go later(); }
main()
`), "tty-refusal.bpp")
	if err != nil {
		t.Fatal(err)
	}
	err = r.Run(context.Background(), f)
	if err == nil || strings.Contains(output.String(), "escaped") ||
		!strings.Contains(output.String(), "blocking input is unavailable") {
		t.Fatalf("out=%q err=%v", output.String(), err)
	}
}

func TestTaskNonblockingReaderAliasDrainAndCancellation(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	defer write.Close()
	raw, err := read.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	aliasFD := -1
	var dupErr error
	if err := raw.Control(func(fd uintptr) { aliasFD, dupErr = unix.Dup(int(fd)) }); err != nil {
		t.Fatal(err)
	}
	if dupErr != nil {
		t.Fatal(dupErr)
	}
	alias := os.NewFile(uintptr(aliasFD), "alias")
	defer alias.Close()
	if _, err := write.WriteString("x"); err != nil {
		t.Fatal(err)
	}
	var one [1]byte
	if _, err := alias.Read(one[:]); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	armed := make(chan struct{})
	reader := taskReadReader(ctx, read, time.Time{}, nil, func() bool {
		select {
		case <-armed:
		default:
			close(armed)
		}
		return true
	})
	if reader == nil {
		t.Fatal("os.Pipe read descriptor was not nonblocking")
	}
	done := make(chan error, 1)
	go func() { _, err := reader.Read(one[:]); done <- err }()
	select {
	case <-armed:
	case <-time.After(time.Second):
		t.Fatal("alias drain did not become EAGAIN and arm")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("read error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("alias-drained read did not cancel")
	}
}

func TestTaskNonblockingReaderEOFAndBlockingRefusal(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	write.Close()
	defer read.Close()
	armed := false
	reader := taskReadReader(context.Background(), read, time.Time{}, nil, func() bool { armed = true; return true })
	if reader == nil {
		t.Fatal("os.Pipe read descriptor was not nonblocking")
	}
	var one [1]byte
	if _, err := reader.Read(one[:]); !errors.Is(err, io.EOF) || armed {
		t.Fatalf("EOF read err=%v armed=%v", err, armed)
	}
	raw, err := read.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var flagErr error
	if err := raw.Control(func(fd uintptr) {
		flags, err := unix.FcntlInt(fd, unix.F_GETFL, 0)
		if err != nil {
			flagErr = err
			return
		}
		_, flagErr = unix.FcntlInt(fd, unix.F_SETFL, flags&^unix.O_NONBLOCK)
	}); err != nil {
		t.Fatal(err)
	}
	if flagErr != nil {
		t.Fatal(flagErr)
	}
	if got := taskReadReader(context.Background(), read, time.Time{}, nil, func() bool { return true }); got != nil {
		t.Fatal("blocking descriptor was accepted for task read")
	}
}

func TestTaskNonblockingReaderReadyDataWinsExpiredTimeout(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	defer write.Close()
	if _, err := write.WriteString("x"); err != nil {
		t.Fatal(err)
	}
	armed := false
	reader := taskReadReader(context.Background(), read, time.Now().Add(-time.Second), nil, func() bool {
		armed = true
		return true
	})
	if reader == nil {
		t.Fatal("os.Pipe read descriptor was not nonblocking")
	}
	var one [1]byte
	if n, err := reader.Read(one[:]); err != nil || n != 1 || one[0] != 'x' || armed {
		t.Fatalf("expired timeout suppressed ready data: n=%d data=%q err=%v armed=%v", n, one[:], err, armed)
	}
}

func TestTaskReadZeroTimeoutIsNonConsumingAndAcceptsHUP(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := write.WriteString("ready"); err != nil {
		t.Fatal(err)
	}
	if !taskReadReadyNow(read) {
		t.Fatal("buffered bytes not reported ready")
	}
	var buf [5]byte
	if _, err := io.ReadFull(read, buf[:]); err != nil || string(buf[:]) != "ready" {
		t.Fatalf("readiness probe consumed data: %q err=%v", buf[:], err)
	}
	write.Close()
	if !taskReadReadyNow(read) {
		t.Fatal("closed empty pipe HUP was not reported ready")
	}
	read.Close()
}

func TestTaskNonblockingReaderSameFileCloseUnblocks(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer write.Close()
	armed := make(chan struct{})
	reader := taskReadReader(context.Background(), read, time.Time{}, nil, func() bool {
		select {
		case <-armed:
		default:
			close(armed)
		}
		return true
	})
	if reader == nil {
		t.Fatal("os.Pipe read descriptor was not nonblocking")
	}
	done := make(chan error, 1)
	go func() { var one [1]byte; _, err := reader.Read(one[:]); done <- err }()
	select {
	case <-armed:
	case <-time.After(time.Second):
		t.Fatal("blocked reader did not arm")
	}
	if err := read.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("closed read unexpectedly succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("same-file close did not release task reader")
	}
}

func TestBashPPTaskSourceFIFORefusedWithoutHang(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blocked.fifo")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var output strings.Builder // bashpp-racegate:safe-synchronized
	var runErr error
	r, err := New(Lang(syntax.LangBashPP), StdIO(nil, &output, &output))
	if err != nil {
		t.Fatal(err)
	}
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(`
func load() { source `+path+`; }
func main() { go load(); }
main()
`), "fifo-source.bpp")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		runErr = r.Run(context.Background(), f)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("task source FIFO blocked File join")
	}
	if runErr == nil || !strings.Contains(output.String(), "FIFO input is unavailable") {
		t.Fatalf("out=%q err=%v", output.String(), runErr)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestBashPPTaskRelativeOpenUsesRetainedDirectory(t *testing.T) {
	root := t.TempDir()
	original := filepath.Join(root, "original")
	moved := filepath.Join(root, "moved")
	if err := os.Mkdir(original, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(original, "payload.bpp"), []byte("echo sourced\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder // bashpp-racegate:safe-synchronized
	r, err := New(Lang(syntax.LangBashPP), StdIO(nil, &output, &output))
	if err != nil {
		t.Fatal(err)
	}
	r.Dir = original
	src := `
func worker(start, ack) {
 <-start
 source payload.bpp
 echo redirected > result.txt
 ack <- ok
}
func main() {
 start := make(chan string)
 ack := make(chan string)
 go worker(start, ack)
 /bin/mv "` + original + `" "` + moved + `"
 start <- go
 <-ack
}
main()
`
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "retained-cwd.bpp")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), f); err != nil {
		t.Fatalf("out=%q err=%v", output.String(), err)
	}
	if output.String() != "sourced\n" {
		t.Fatalf("relative source missed retained directory: %q", output.String())
	}
	data, err := os.ReadFile(filepath.Join(moved, "result.txt"))
	if err != nil || string(data) != "redirected\n" {
		t.Fatalf("relative redirection missed retained directory: data=%q err=%v", data, err)
	}
	if _, err := os.Stat(original); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("original cwd pathname unexpectedly remains: %v", err)
	}
}
