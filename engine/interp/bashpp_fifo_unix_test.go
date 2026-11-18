// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

//go:build unix

package interp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/syntax"
	"golang.org/x/sys/unix"
)

func TestBashPPFIFORetryInterruptedAcquisition(t *testing.T) {
	n := 0
	if err := bashPPFIFORetry(context.Background(), func() error {
		n++
		if n < 3 {
			return unix.EINTR
		}
		return nil
	}); err != nil || n != 3 {
		t.Fatalf("calls=%d err=%v", n, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	n = 0
	err := bashPPFIFORetry(ctx, func() error {
		n++
		cancel()
		return unix.EINTR
	})
	if !errors.Is(err, context.Canceled) || n != 1 {
		t.Fatalf("calls=%d err=%v", n, err)
	}
}

// The statement hot path asks this predicate while FIFO registration and
// retirement hold fifoMu. It must therefore use the registration count's
// atomic snapshot rather than contend on the registry lock.
func TestBashPPHasFIFOsDoesNotLockRegistry(t *testing.T) {
	c := newBashPPConcurrent(context.Background())
	defer c.cancel()
	r := &Runner{bashPPConcurrent: c}

	c.fifoMu.Lock()
	defer c.fifoMu.Unlock()
	done := make(chan bool, 1)
	go func() { done <- r.bashPPHasFIFOs() }()
	select {
	case got := <-done:
		if got {
			t.Fatal("empty FIFO registry reported registrations")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("bashPPHasFIFOs waited for the FIFO registry lock")
	}
}

func TestBashPPFIFOWrappedWriterSnapshot(t *testing.T) {
	c := newBashPPConcurrent(context.Background())
	defer c.cancel()
	defer c.closeFIFOs(nil)
	reader, owner := newFIFOTestTask(t, c), newFIFOTestTask(t, c)
	path := fifoTestPath(t)
	done := startFIFOTestOpen(reader, path, os.O_RDONLY)
	<-reader.bashPPTaskState.ready
	fifoTestStmt(t, owner, fmt.Sprintf("exec 8>%q", path))
	read := awaitFIFOTestOpen(t, done)
	if read.err != nil {
		t.Fatal(read.err)
	}
	original := owner.fdWriteTable[8].(fifoWriteFile).File
	var mu sync.Mutex
	wrapped := &bashPPLockedWriter{mu: &mu, w: &pipelineWriter{w: owner.fdWriteTable[8], runner: owner}}
	owner.fdWriteTable[8], owner.stdout = wrapped, wrapped
	owner.stderr = borrowedFile{File: original}
	child, err := owner.bashPPTaskSnapshot(10, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer child.closeBashPPTaskResources()
	childLocked := child.stdout.(*bashPPLockedWriter)
	childPipeline := childLocked.w.(*pipelineWriter)
	duplicate := childPipeline.w.(fifoWriteFile).File
	if duplicate == original || childPipeline.runner != child || childLocked.mu != &mu {
		t.Fatal("snapshot borrowed the parent's descriptor or lost wrapper ownership")
	}
	if child.fdWriteTable[8].(*bashPPLockedWriter).w.(*pipelineWriter).w.(fifoWriteFile).File != duplicate ||
		child.stderr.(borrowedFile).File != duplicate {
		t.Fatal("writer aliases did not share one task-owned duplicate")
	}
	fifoTestStmt(t, owner, "exec 8>&- 1>&- 2>&-")
	fifoTestRegistration(t, c, original, false)
	fifoTestRegistration(t, c, duplicate, true)
	if _, err := io.WriteString(child.fdWriteTable[8], "owned"); err != nil {
		t.Fatal(err)
	}
	var buf [5]byte
	if _, err := io.ReadFull(read.file, buf[:]); err != nil || string(buf[:]) != "owned" {
		t.Fatalf("data=%q err=%v", buf, err)
	}
	fifoTestStmt(t, child, "exec 8>&- 1>&-")
	fifoTestRegistration(t, c, duplicate, true)
	fifoTestStmt(t, child, "exec 2>&-")
	fifoTestRegistration(t, c, duplicate, false)
}

func TestBashPPFIFOFileOwnsPreTaskPersistentDescriptor(t *testing.T) {
	r, err := New(Lang(syntax.LangBashPP))
	if err != nil {
		t.Fatal(err)
	}
	defer r.closeDirFile()
	var file *os.File
	var group *bashPPConcurrent
	r.callHandler = func(_ context.Context, args []string) ([]string, error) {
		if args[0] == "probe" {
			file, group = r.fdTable[8], r.bashPPConcurrent
			if group == nil {
				t.Fatal("FIFO opened before go/chan has no File owner")
			}
			fifoTestRegistration(t, group, file, true)
			return []string{":"}, nil
		}
		return args, nil
	}
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(fmt.Sprintf("exec 8<>%q; probe", fifoTestPath(t))), "file-owner")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	if file == nil || group == nil {
		t.Fatal("ownership assertion was not reached")
	}
	fifoTestRegistration(t, group, file, false)
	if r.bashPPConcurrent != nil {
		t.Fatal("completed File retained its FIFO group")
	}
}

func TestBashPPFIFOReaderKeepsAcquiredDescriptor(t *testing.T) {
	c := newBashPPConcurrent(context.Background())
	defer c.cancel()
	defer c.closeFIFOs(nil)
	reader, writer := newFIFOTestTask(t, c), newFIFOTestTask(t, c)
	path := fifoTestPath(t)
	done := startFIFOTestOpen(reader, path, os.O_RDONLY)
	<-reader.bashPPTaskState.ready
	var acquired *os.File
	c.fifoMu.Lock()
	for _, e := range c.fifos {
		acquired = e.file
	}
	c.fifoMu.Unlock()
	written := awaitFIFOTestOpen(t, startFIFOTestOpen(writer, path, os.O_WRONLY))
	if written.err != nil {
		t.Fatal(written.err)
	}
	if _, err := io.WriteString(written.file, "retained"); err != nil {
		t.Fatal(err)
	}
	_ = writer.bashPPFIFOCloser(written.file).Close()
	read := awaitFIFOTestOpen(t, done)
	if read.err != nil || read.file != acquired {
		t.Fatalf("reader reopened its acquired inode: file=%v err=%v", read.file, read.err)
	}
	data, err := io.ReadAll(read.file)
	if err != nil || string(data) != "retained" {
		t.Fatalf("data=%q err=%v", data, err)
	}
}

// Run individual statements without File teardown: cleanup at EOF would hide
// stale registrations left behind by persistent descriptor close/replacement.
func fifoTestStmt(t *testing.T, r *Runner, src string) {
	t.Helper()
	r.fillExpandConfig(r.bashPPConcurrent.ctx)
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "fifo-lifetime")
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range f.Stmts {
		r.stmtSync(r.bashPPConcurrent.ctx, stmt)
		if !r.exit.ok() {
			t.Fatalf("%s: exit %+v", src, r.exit)
		}
	}
}

func fifoTestRegistration(t *testing.T, c *bashPPConcurrent, f *os.File, live bool) {
	t.Helper()
	c.fifoMu.Lock()
	registered := c.fifos[f] != nil
	count, length := c.fifoCount.Load(), len(c.fifos)
	c.fifoMu.Unlock()
	if count != int32(length) {
		t.Fatalf("FIFO count=%d, want registry length %d", count, length)
	}
	_, err := f.Stat()
	if registered != live || (live && err != nil) || (!live && !errors.Is(err, os.ErrClosed)) {
		t.Fatalf("live=%v: registered=%v, stat=%v", live, registered, err)
	}
}

func TestBashPPFIFOPersistentDescriptorLifetime(t *testing.T) {
	for _, replacement := range []string{"close", "fifo", "regular", "twice"} {
		t.Run(replacement, func(t *testing.T) {
			c := newBashPPConcurrent(context.Background())
			defer c.cancel()
			defer c.closeFIFOs(nil)
			r := newFIFOTestTask(t, c)
			a, b := fifoTestPath(t), fifoTestPath(t)
			fifoTestStmt(t, r, fmt.Sprintf("exec 8<>%q", a))
			original := r.fdTable[8]
			fifoTestRegistration(t, c, original, true)
			switch replacement {
			case "close":
				fifoTestStmt(t, r, "exec 8>&-")
			case "fifo":
				fifoTestStmt(t, r, fmt.Sprintf("exec 8<>%q", b))
			case "regular":
				fifoTestStmt(t, r, fmt.Sprintf("exec 8<>%q", filepath.Join(t.TempDir(), "regular")))
				defer r.fdTable[8].Close()
			case "twice":
				fifoTestStmt(t, r, fmt.Sprintf("exec 8<>%q 8<>%q", a, b))
			}
			fifoTestRegistration(t, c, original, false)
			if replacement == "fifo" || replacement == "twice" {
				current := r.fdTable[8]
				fifoTestRegistration(t, c, current, true)
				c.fifoMu.Lock()
				n := len(c.fifos)
				c.fifoMu.Unlock()
				if n != 1 {
					t.Fatalf("retained %d registrations after replacement", n)
				}
				fifoTestStmt(t, r, "exec 8>&-")
				fifoTestRegistration(t, c, current, false)
			}
		})
	}
}

func TestBashPPFIFOPersistentAliasesAndSnapshot(t *testing.T) {
	c := newBashPPConcurrent(context.Background())
	defer c.cancel()
	defer c.closeFIFOs(nil)
	r := newFIFOTestTask(t, c)
	fifoTestStmt(t, r, fmt.Sprintf("exec 8<>%q; exec 9<&8", fifoTestPath(t)))
	original := r.fdTable[8]
	child, err := r.bashPPTaskSnapshot(10, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer child.closeBashPPTaskResources()
	duplicate := child.fdTable[8]
	fifoTestStmt(t, r, "exec 8>&-")
	fifoTestRegistration(t, c, original, true)
	fifoTestStmt(t, r, "exec 9>&-")
	fifoTestRegistration(t, c, original, false)
	fifoTestRegistration(t, c, duplicate, true)
	fifoTestStmt(t, child, "exec 8>&-")
	fifoTestRegistration(t, c, duplicate, true)
	fifoTestStmt(t, child, "exec 9>&-")
	fifoTestRegistration(t, c, duplicate, false)
}

func TestBashPPFIFOTemporaryScopeAndEscapingAlias(t *testing.T) {
	c := newBashPPConcurrent(context.Background())
	defer c.cancel()
	defer c.closeFIFOs(nil)
	r := newFIFOTestTask(t, c)
	a, b := fifoTestPath(t), fifoTestPath(t)
	fifoTestStmt(t, r, fmt.Sprintf("exec 8<>%q", a))
	original := r.fdTable[8]
	fifoTestStmt(t, r, fmt.Sprintf("{ exec 8>&-; } 8<>%q", b))
	if r.fdTable[8] != original {
		t.Fatal("temporary redirect did not restore the original binding")
	}
	fifoTestRegistration(t, c, original, true)
	fifoTestStmt(t, r, "exec 8>&-")
	fifoTestRegistration(t, c, original, false)
	fifoTestStmt(t, r, fmt.Sprintf("{ exec 9<&0; } <>%q", a))
	escaped := r.fdTable[9]
	fifoTestRegistration(t, c, escaped, true)
	fifoTestStmt(t, r, "exec 9>&-")
	fifoTestRegistration(t, c, escaped, false)
	fifoTestStmt(t, r, fmt.Sprintf("8<>%q", a))
	c.fifoMu.Lock()
	n := len(c.fifos)
	c.fifoMu.Unlock()
	if n != 0 {
		t.Fatalf("redirection-only temporary runner retained %d registrations", n)
	}
}

func TestBashPPFIFOCloseInsideUnrelatedBoundary(t *testing.T) {
	c := newBashPPConcurrent(context.Background())
	defer c.cancel()
	defer c.closeFIFOs(nil)
	r := newFIFOTestTask(t, c)
	r.bashPPGoTask = false // The owner may redirect its diagnostic stream to /dev/null.
	fifoTestStmt(t, r, fmt.Sprintf("exec 8<>%q", fifoTestPath(t)))
	original := r.fdTable[8]
	checked := false
	r.callHandler = func(_ context.Context, args []string) ([]string, error) {
		if args[0] == "probe" {
			fifoTestRegistration(t, c, original, false)
			checked = true
			return []string{":"}, nil
		}
		return args, nil
	}
	fifoTestStmt(t, r, "{ exec 8>&-; probe; } >/dev/null")
	if !checked {
		t.Fatal("inner boundary assertion did not execute")
	}
}

func newFIFOTestTask(t *testing.T, c *bashPPConcurrent) *Runner {
	t.Helper()
	r, err := New(Lang(syntax.LangBashPP))
	if err != nil {
		t.Fatal(err)
	}
	r.Reset()
	r.bashPPConcurrent, r.bashPPGoTask = c, true
	r.bashPPTaskState, _ = c.add()
	t.Cleanup(r.closeDirFile)
	return r
}

func fifoTestPath(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "rendezvous.fifo")
	if err := unix.Mkfifo(p, 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

type fifoOpenResult struct {
	file io.ReadWriteCloser
	err  error
}

func startFIFOTestOpen(r *Runner, path string, flags int) <-chan fifoOpenResult {
	done := make(chan fifoOpenResult, 1)
	go func() {
		f, err := r.bashPPTaskOpen(r.bashPPConcurrent.ctx, path, flags, 0600, false, false)
		done <- fifoOpenResult{f, err}
	}()
	return done
}

func awaitFIFOTestOpen(t *testing.T, done <-chan fifoOpenResult) fifoOpenResult {
	t.Helper()
	select {
	case res := <-done:
		return res
	case <-time.After(2 * time.Second):
		t.Fatal("FIFO opener did not join")
		return fifoOpenResult{}
	}
}

func TestBashPPFIFORendezvous(t *testing.T) {
	for _, writerFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "reader-first", true: "writer-first"}[writerFirst], func(t *testing.T) {
			path := fifoTestPath(t)
			c := newBashPPConcurrent(context.Background())
			defer c.cancel()
			defer c.closeFIFOs(nil)
			reader, writer := newFIFOTestTask(t, c), newFIFOTestTask(t, c)
			first, second, firstFlags, secondFlags := reader, writer, os.O_RDONLY, os.O_WRONLY
			if writerFirst {
				first, second, firstFlags, secondFlags = writer, reader, os.O_WRONLY, os.O_RDONLY
			}
			a := startFIFOTestOpen(first, path, firstFlags)
			<-first.bashPPTaskState.ready
			c.fifoMu.Lock()
			pending := len(c.fifos) == 1
			for _, e := range c.fifos {
				pending = pending && !e.matched
			}
			c.fifoMu.Unlock()
			if !pending {
				t.Fatal("first endpoint was released without a registered peer")
			}
			b := startFIFOTestOpen(second, path, secondFlags)
			ra, rb := awaitFIFOTestOpen(t, a), awaitFIFOTestOpen(t, b)
			if ra.err != nil || rb.err != nil {
				t.Fatalf("opens: %v, %v", ra.err, rb.err)
			}
			rf, wf := ra.file, rb.file
			if writerFirst {
				rf, wf = rb.file, ra.file
			}
			rc, wc := reader.bashPPFIFOCloser(rf), writer.bashPPFIFOCloser(wf)
			if _, err := io.WriteString(wf, "ready\n"); err != nil {
				t.Fatal(err)
			}
			if err := wc.Close(); err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(rf)
			if err != nil || string(data) != "ready\n" {
				t.Fatalf("data=%q err=%v", data, err)
			}
			if err := rc.Close(); err != nil {
				t.Fatal(err)
			}
			c.fifoMu.Lock()
			n := len(c.fifos)
			c.fifoMu.Unlock()
			if n != 0 {
				t.Fatalf("retained %d FIFO registrations", n)
			}
		})
	}
}

func TestBashPPFIFOCancellationClosesPendingDescriptors(t *testing.T) {
	for _, flags := range []int{os.O_RDONLY, os.O_WRONLY} {
		t.Run(map[int]string{os.O_RDONLY: "reader", os.O_WRONLY: "writer"}[flags], func(t *testing.T) {
			c := newBashPPConcurrent(context.Background())
			defer c.cancel()
			r := newFIFOTestTask(t, c)
			done := startFIFOTestOpen(r, fifoTestPath(t), flags)
			<-r.bashPPTaskState.ready
			var files []*os.File
			c.fifoMu.Lock()
			for _, e := range c.fifos {
				files = append(files, e.file)
				if e.probe != nil {
					files = append(files, e.probe)
				}
			}
			c.fifoMu.Unlock()
			c.cancel()
			res := awaitFIFOTestOpen(t, done)
			if res.file != nil || !errors.Is(res.err, context.Canceled) {
				t.Fatalf("file=%v err=%v", res.file, res.err)
			}
			for _, f := range files {
				if _, err := f.Stat(); !errors.Is(err, os.ErrClosed) {
					t.Fatalf("descriptor remains open: %v", err)
				}
			}
			c.fifoMu.Lock()
			n := len(c.fifos)
			c.fifoMu.Unlock()
			if n != 0 {
				t.Fatalf("retained %d FIFO registrations", n)
			}
		})
	}
}

func TestBashPPFIFOExternalPeerCannotReleaseOpen(t *testing.T) {
	path := fifoTestPath(t)
	external, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer external.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	c := newBashPPConcurrent(ctx)
	defer c.cancel()
	r := newFIFOTestTask(t, c)
	file, err := r.bashPPTaskOpen(ctx, path, os.O_RDONLY, 0, false, false)
	if file != nil {
		_ = file.Close()
		t.Fatal("unregistered native writer released the task FIFO open")
	}
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "external or unregistered peers are unsupported") {
		t.Fatalf("err=%v", err)
	}
}

func TestBashPPFIFOReplacementDoesNotMatchOldInode(t *testing.T) {
	path := fifoTestPath(t)
	c := newBashPPConcurrent(context.Background())
	defer c.cancel()
	reader, writer := newFIFOTestTask(t, c), newFIFOTestTask(t, c)
	a := startFIFOTestOpen(reader, path, os.O_RDONLY)
	<-reader.bashPPTaskState.ready
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	b := startFIFOTestOpen(writer, path, os.O_WRONLY)
	<-writer.bashPPTaskState.ready
	c.fifoMu.Lock()
	unmatched := len(c.fifos) == 2
	keys := make(map[bashPPFIFOIdentity]bool)
	for _, e := range c.fifos {
		unmatched = unmatched && !e.matched
		keys[e.key] = true
	}
	c.fifoMu.Unlock()
	if !unmatched || len(keys) != 2 {
		t.Fatal("different FIFO inodes shared a rendezvous")
	}
	c.cancel()
	for _, done := range []<-chan fifoOpenResult{a, b} {
		if res := awaitFIFOTestOpen(t, done); !errors.Is(res.err, context.Canceled) {
			t.Fatalf("err=%v", res.err)
		}
	}
}

func TestBashPPFIFOTaskSnapshotOwnsRegistration(t *testing.T) {
	c := newBashPPConcurrent(context.Background())
	defer c.cancel()
	defer c.closeFIFOs(nil)
	owner := newFIFOTestTask(t, c)
	path := fifoTestPath(t)
	file, err := owner.bashPPTaskOpen(c.ctx, path, os.O_RDWR, 0, false, false)
	if err != nil {
		t.Fatal(err)
	}
	owner.stdin = file.(*os.File)
	child, err := owner.bashPPTaskSnapshot(10, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer child.closeBashPPTaskResources()
	c.closeFIFOs(owner)
	if _, err := child.stdin.Stat(); err != nil {
		t.Fatalf("original close invalidated task duplicate: %v", err)
	}
	writer := newFIFOTestTask(t, c)
	res := awaitFIFOTestOpen(t, startFIFOTestOpen(writer, path, os.O_WRONLY))
	if res.err != nil {
		t.Fatal(res.err)
	}
	if _, err := io.WriteString(res.file, "x"); err != nil {
		t.Fatal(err)
	}
	var b [1]byte
	if _, err := io.ReadFull(child.stdin, b[:]); err != nil || b[0] != 'x' {
		t.Fatalf("read=%q err=%v", b, err)
	}
	if err := writer.bashPPFIFOCloser(res.file).Close(); err != nil {
		t.Fatal(err)
	}
	child.closeBashPPTaskResources()
	if _, err := child.stdin.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("task duplicate remains open: %v", err)
	}
	c.fifoMu.Lock()
	n := len(c.fifos)
	c.fifoMu.Unlock()
	if n != 0 {
		t.Fatalf("retained %d FIFO registrations", n)
	}
}

func TestBashPPFIFOCancellationPreservesPrimaryFailure(t *testing.T) {
	for _, command := range []string{"/bin/cat < FIFO", "echo escaped > FIFO"} {
		path := fifoTestPath(t)
		out, err := runBashPPConcurrency(t, strings.ReplaceAll(`
func blocked() { COMMAND; }
func fail() { return 7; }
func main() { go blocked(); go fail(); }
main()
`, "COMMAND", strings.ReplaceAll(command, "FIFO", path)))
		if err == nil || !strings.Contains(out, "exit status 7") || strings.Contains(out, "exit status 1") || strings.Contains(out, "escaped") {
			t.Fatalf("out=%q err=%v", out, err)
		}
	}
}

func TestBashPPFIFOPreservesOwnerSpecialFiles(t *testing.T) {
	r, err := New(Lang(syntax.LangBashPP), StdIO(strings.NewReader("owner-input"), nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	r.Reset()
	defer r.closeDirFile()
	c := newBashPPConcurrent(context.Background())
	defer c.cancel()
	r.bashPPConcurrent = c
	f, err := r.bashPPTaskOpen(c.ctx, "/dev/stdin", os.O_RDONLY, 0, false, false)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(f)
	if err != nil || string(data) != "owner-input" {
		t.Fatalf("stdin=%q err=%v", data, err)
	}
	_ = f.Close()
	_ = r.stdin.Close()
	f, err = r.bashPPTaskOpen(c.ctx, os.DevNull, os.O_WRONLY, 0, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("ignored")); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
}

// A published endpoint — one whose blocking open completed natively — releases
// a rendezvous opener of the opposite end, including one that has announced
// itself but not yet registered a descriptor: the acquisition is what released
// the publisher's open, so the announcement is what closes the window in which
// the publisher could run to completion unseen. A rendezvous opener never
// releases an announced peer, since that peer holds no descriptor yet.
func TestBashPPFIFOPublishedPeerReleasesAnnouncedOpener(t *testing.T) {
	path := fifoTestPath(t)
	c := newBashPPConcurrent(context.Background())
	defer c.cancel()
	defer c.closeFIFOs(nil)
	key, fifo, err := bashPPFIFOIdentify(c.ctx, nil, path)
	if err != nil || !fifo {
		t.Fatalf("fifo=%v err=%v", fifo, err)
	}
	reader := newFIFOTestTask(t, c)
	announced := &bashPPFIFOEntry{group: c, owner: reader, key: key, path: path, read: true, ready: make(chan struct{})}
	c.fifoMu.Lock()
	c.fifoPending = map[*bashPPFIFOEntry]struct{}{announced: {}}
	c.fifoMu.Unlock()

	// A rendezvous writer sees no descriptor to pair with and waits.
	writer := newFIFOTestTask(t, c)
	pending := startFIFOTestOpen(writer, path, os.O_WRONLY)
	<-writer.bashPPTaskState.ready
	c.fifoMu.Lock()
	released := announced.matched
	c.fifoMu.Unlock()
	if released {
		t.Fatal("rendezvous writer released an opener that holds no descriptor")
	}

	// A published writer releases it at once.
	owner := newFIFOTestTask(t, c)
	owner.bashPPGoTask = false
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	e := owner.bashPPFIFOPublish(c, file, path, os.O_WRONLY)
	if e == nil {
		t.Fatal("FIFO descriptor was not published")
	}
	select {
	case <-announced.ready:
	default:
		t.Fatal("published writer did not release the announced reader")
	}
	// A published reader pairs with the acquired rendezvous writer as well.
	reading, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	re := owner.bashPPFIFOPublish(c, reading, path, os.O_RDONLY)
	if re == nil {
		t.Fatal("FIFO descriptor was not published")
	}
	if res := awaitFIFOTestOpen(t, pending); res.err != nil {
		t.Fatal(res.err)
	} else {
		_ = writer.bashPPFIFOCloser(res.file).Close()
	}
	for _, e := range []*bashPPFIFOEntry{e, re} {
		if err := e.Close(); err != nil {
			t.Fatal(err)
		}
	}
	c.fifoMu.Lock()
	delete(c.fifoPending, announced)
	n := len(c.fifos)
	c.fifoMu.Unlock()
	if n != 0 {
		t.Fatalf("retained %d FIFO registrations", n)
	}
}

// End to end: a task's redirection over a FIFO whose writer is a process
// substitution — natively opened, then published — joins and reads the data.
func TestBashPPFIFOTaskRedirectOverProcessSubstitution(t *testing.T) {
	out, err := runBashPPConcurrency(t, `
func consume(ack) { /bin/cat < <(echo published); ack <- ok; }
func main() {
 ack := make(chan string)
 go consume(ack)
 <-ack
}
main()
`)
	if err != nil || out != "published\n" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

// Sprint 119 story 0f7d95e5cf1f: process substitution must not deadlock when
// the Bash++ dialect is active but the shell has not started a Go task.
func TestBashPPProcessSubstitutionNoDeadlock(t *testing.T) {
	for _, tc := range []struct {
		name, source, want string
	}{
		{
			name:   "input-redirection",
			source: `read -r x < <(echo hi); printf 'got=%s\n' "$x"`,
			want:   "got=hi\n",
		},
		{
			name:   "output-redirection",
			source: `echo hi > >(cat); wait`,
			want:   "hi\n",
		},
		{
			name:   "fd-redirection",
			source: `exec 3< <(echo hi); read -r x <&3; printf 'got=%s\n' "$x"`,
			want:   "got=hi\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder // bashpp-racegate:safe-synchronized the Bash++ group wraps task writers in bashPPLockedWriter; Run joins the tasks before out is read.
			r, err := New(Lang(syntax.LangBashPP), StdIO(nil, &out, &out))
			if err != nil {
				t.Fatal(err)
			}
			f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(tc.source), tc.name+".bpp")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := r.Run(ctx, f); err != nil {
				t.Fatalf("run: %v; output=%q", err, out.String())
			}
			if got := out.String(); got != tc.want {
				t.Fatalf("output=%q, want %q", got, tc.want)
			}
		})
	}
}
