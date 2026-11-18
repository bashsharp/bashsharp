// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

//go:build windows

package interp

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"strings"
	"testing"
	"time"
)

// A Windows named pipe is where "what does a second open observe?" stopped
// being rhetorical: it used to be answered with ERROR_PIPE_BUSY, "All pipe
// instances are busy", which is how `diff <(a) <(b)` failed — its operands
// are stat'ed and then opened, and a single-instance pipe has only one
// connection to give.
//
// The answer these pin is bash's own on Linux, where `<(cmd)` is one
// /dev/fd pipe the shell holds open: a second handle on it reads whatever
// is left of the stream, which is nothing once the first reader drained it.
// Never an error, and never a replay — Sprint 245's replay buffer (sh
// b878da63, reverted as 34583180) served every reader the whole body, which
// did not fix this and broke reading a substitution once.

// openPipe is os.OpenFile in the direction the substituted path is meant to
// be opened in.
func openPipe(t *testing.T, path string) (*os.File, error) {
	t.Helper()
	return os.OpenFile(path, os.O_RDONLY, 0)
}

// The single-reader case, which must be exactly what it always was: one
// open, the whole body, EOF. Bigger than the pipe's 4 KiB buffer so the
// substitution's writes genuinely block on the reader.
func TestProcSubstPipeReadOnce(t *testing.T) {
	body := strings.Repeat("the quick brown fox\n", 5000)

	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.newProcSubstPipe(true)
	if err != nil {
		t.Fatal(err)
	}
	defer p.cleanup()

	wrote := make(chan error, 1)
	go func() {
		f, err := p.openWriter()
		if err != nil {
			wrote <- err
			return
		}
		_, err = io.WriteString(f, body)
		f.Close()
		wrote <- err
	}()

	f, err := openPipe(t, p.path())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	got, err := io.ReadAll(f)
	f.Close()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != body {
		t.Errorf("read %d bytes, want %d", len(got), len(body))
	}
	if err := <-wrote; err != nil {
		t.Errorf("substitution write: %v", err)
	}
}

// A second open, once the first reader drained the substitution: it opens,
// and it reads end-of-stream.
func TestProcSubstPipeSecondOpenReadsEndOfStream(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.newProcSubstPipe(true)
	if err != nil {
		t.Fatal(err)
	}
	defer p.cleanup()

	go func() {
		f, err := p.openWriter()
		if err != nil {
			return
		}
		io.WriteString(f, "hello\n")
		f.Close()
	}()

	f1, err := openPipe(t, p.path())
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	got, err := io.ReadAll(f1)
	f1.Close()
	if err != nil || string(got) != "hello\n" {
		t.Fatalf("first read = %q, %v", got, err)
	}

	for i := range 3 {
		f, err := openPipe(t, p.path())
		if err != nil {
			t.Fatalf("open %d after the stream was drained: %v", i+2, err)
		}
		got, err := io.ReadAll(f)
		f.Close()
		if err != nil {
			t.Errorf("read %d: %v", i+2, err)
		}
		if len(got) != 0 {
			t.Errorf("read %d = %q, want end-of-stream", i+2, got)
		}
	}
}

// The shape that failed: both opens happen before either is read, as a
// consumer that stats a path and then opens it does. Neither may be
// refused, and the two together must not deadlock.
func TestProcSubstPipeTwoOpensBeforeAnyRead(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.newProcSubstPipe(true)
	if err != nil {
		t.Fatal(err)
	}
	defer p.cleanup()

	go func() {
		f, err := p.openWriter()
		if err != nil {
			return
		}
		io.WriteString(f, "hello\n")
		f.Close()
	}()

	f1, err := openPipe(t, p.path())
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	defer f1.Close()
	f2, err := openPipe(t, p.path())
	if err != nil {
		t.Fatalf("second open, with the first still held: %v", err)
	}
	defer f2.Close()

	// The stream went to whichever open reached the shell first; the other
	// is at its end. Both reads must finish, and between them they see the
	// body exactly once.
	var seen bytes.Buffer
	for _, f := range []*os.File{f2, f1} {
		b, err := io.ReadAll(f)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		seen.Write(b)
	}
	if seen.String() != "hello\n" {
		t.Errorf("the two reads together saw %q, want the body exactly once", seen.String())
	}
}

// Opening more times than the warm-listener count proves that the count is
// overlap tolerance rather than a lifetime cap. Each accepted connection must
// replenish its listener before the next external CreateFile arrives.
func TestProcSubstPipeReplenishesListeners(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.newProcSubstPipe(true)
	if err != nil {
		t.Fatal(err)
	}
	defer p.cleanup()

	go func() {
		f, err := p.openWriter()
		if err != nil {
			return
		}
		io.WriteString(f, "once\n")
		f.Close()
	}()

	const opens = 8
	files := make([]*os.File, 0, opens)
	for i := range opens {
		f, err := openPipe(t, p.path())
		if err != nil {
			t.Fatalf("open %d: %v", i+1, err)
		}
		files = append(files, f)
	}
	var seen bytes.Buffer
	for i, f := range files {
		b, err := io.ReadAll(f)
		f.Close()
		if err != nil {
			t.Fatalf("read %d: %v", i+1, err)
		}
		seen.Write(b)
	}
	if seen.String() != "once\n" {
		t.Fatalf("all opens together saw %q, want the stream once", seen.String())
	}
}

// Once the shell releases the pipe the name is gone: an open fails, and the
// shell's own stat of the path reports it missing rather than inventing a
// FIFO that is no longer there. Both are what the unlinked FIFO does on
// Unix.
func TestProcSubstPipeGoneAfterCleanup(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.newProcSubstPipe(true)
	if err != nil {
		t.Fatal(err)
	}
	if info, ok := procSubstPipeStat(p.path()); !ok || info == nil {
		t.Fatalf("a live pipe does not stat: ok=%v info=%v", ok, info)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		f, err := p.openWriter()
		if err != nil {
			return
		}
		io.WriteString(f, "hello\n")
		f.Close()
	}()
	f, err := openPipe(t, p.path())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	io.ReadAll(f)
	f.Close()
	<-done

	p.cleanup()
	if _, err := openPipe(t, p.path()); err == nil {
		t.Error("the path still opens after cleanup released it")
	}
	if info, ok := procSubstPipeStat(p.path()); !ok || info != nil {
		t.Errorf("a released pipe stats as %v (ok=%v)", info, ok)
	}
	// cleanup is idempotent; the runner calls it on its error paths too.
	p.cleanup()
}

// `>(cmd)`, where the shell reads and the consumer writes, takes the same
// accept loop: the first connection is the substitution's stdin, and the
// shell reads it to EOF.
func TestProcSubstPipeWriteDirection(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.newProcSubstPipe(false)
	if err != nil {
		t.Fatal(err)
	}
	defer p.cleanup()

	read := make(chan string, 1)
	go func() {
		f, err := p.openReader()
		if err != nil {
			read <- "open: " + err.Error()
			return
		}
		b, _ := io.ReadAll(f)
		f.Close()
		read <- string(b)
	}()

	f, err := os.OpenFile(p.path(), os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open for write: %v", err)
	}
	if _, err := io.WriteString(f, "fed\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	f.Close()
	if got := <-read; got != "fed\n" {
		t.Errorf("the substitution read %q", got)
	}
}

// cleanup must take the waiting instances with it even when nothing ever
// connected, or the name — and a goroutine per substitution — would outlive
// the shell that made it. Bash's procsub test opens several hundred in a
// loop.
func TestProcSubstPipeCleanupWithNoConsumer(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	for i := range 50 {
		p, err := r.newProcSubstPipe(i%2 == 0)
		if err != nil {
			t.Fatal(err)
		}
		// Serving starts at construction so an external consumer cannot win
		// a scheduling race. Also wait for the substitution's end here,
		// without a consumer, to cover both connect directions and stopCh.
		if i%2 == 0 {
			go p.openWriter()
		} else {
			go p.openReader()
		}
		time.Sleep(time.Millisecond)
		p.cleanup()
		// The name itself must be gone, not merely unopenable in this
		// direction: only ERROR_FILE_NOT_FOUND says the last instance went.
		_, err = os.OpenFile(p.path(), os.O_RDONLY, 0)
		if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("the name outlived cleanup: open reports %v", err)
		}
	}
}
