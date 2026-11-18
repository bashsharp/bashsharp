// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp

import (
	"bytes"
	"context"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// What a second open of one process substitution observes.
//
// Bash on Linux answers the question with /dev/fd/N: `<(cmd)` names one
// pipe the shell holds open, so opening the path again is another handle on
// the same stream and reads whatever is left of it — nothing, once the
// first reader drained it. Its own procsub test pins that: five reads of
// one `<(date)` print 1, 0, 0, 0, 0, and the `[ -e ]` between them is what
// tells a live substitution from a spent one, because the FIFO of a
// finished substitution has been unlinked.
//
// These pin both halves of that on every host. The Windows named pipe is
// covered by procsubst_secondopen_windows_test.go, which is where a second
// open used to fail outright with "All pipe instances are busy".

// runProcSubstScript runs src with a fresh runner and returns its combined
// output. A substitution that never gets read would block forever, so the
// run is bounded.
func runProcSubstScript(t *testing.T, src string) string {
	t.Helper()
	file, err := syntax.NewParser().Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	r, err := New(StdIO(nil, &out, &out))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := r.Run(ctx, file); err != nil {
		t.Fatalf("running %q: %v (output so far: %q)", src, err, out.String())
	}
	if ctx.Err() != nil {
		t.Fatalf("running %q timed out; a substitution was never served (output so far: %q)",
			src, out.String())
	}
	return out.String()
}

// A substitution that is read exactly once is the overwhelmingly common
// case and the one a second-open mechanism must not disturb: Sprint 245's
// replay buffer broke it, taking histexpand and new-exp with it and pushing
// procsub's failure back to its very first line.
func TestProcSubstReadOnceStillWorks(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		src  string
		want string
	}{
		{"cat", "cat <(echo test1)", "test1\n"},
		{"eval", "eval cat <(echo test1)", "test1\n"},
		{"two operands", "cat <(echo test6) <(echo test7)", "test6\ntest7\n"},
		{"redirect", "read -r x < <(echo test4); echo $x", "test4\n"},
		// The histexpand / new-exp shape: a numbered descriptor read to EOF
		// and closed, with nothing else ever opening the path.
		{"numbered fd", "while read -ru3 x; do echo -n :; done 3< <(echo x; echo y)", "::"},
		{"source", ". <(echo 'echo sourced')", "sourced\n"},
		{"command substitution", "echo $(cat <(echo nested))", "nested\n"},
		{"write direction", "echo hi > >(cat); wait", "hi\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := runProcSubstScript(t, tc.src); got != tc.want {
				t.Errorf("%s = %q, want %q", tc.src, got, tc.want)
			}
		})
	}
}

// The same substitution read once in a tight loop: bash's own procsub test
// does this several hundred times to prove the shell is not leaking the
// rendezvous. Each iteration must serve its one reader and then let the
// name go.
func TestProcSubstReadOnceInALoop(t *testing.T) {
	t.Parallel()
	const src = `i=0
while [ $i -lt 60 ]; do
	while read -ru3 x; do echo -n :; done 3< <(echo x)
	i=$((i+1))
done
echo`
	got := runProcSubstScript(t, src)
	if want := strings.Repeat(":", 60) + "\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// count_lines, from bash's procsub test: the substitution is read, and then
// the script asks whether the path is still there before reading it again.
// Once the substitution has finished and the shell released the pipe, it is
// not — `[ -e ]` of the path is false, exactly as it is for the FIFO
// cleanup unlinked on Unix, and the reads that follow it in the fixture are
// the ones the escape hatch answers with zeros.
//
// The release happens on the substitution's own goroutine, just after the
// reader saw EOF, so the script spins rather than assuming it has already
// been scheduled; what is pinned is that the path goes away, not when.
func TestProcSubstSpentSubstitutionIsGone(t *testing.T) {
	t.Parallel()
	const src = `count_lines() {
	while read -r _; do echo -n .; done < "$1"
	echo
	n=0
	while [ -e "$1" ] && [ $n -lt 100000 ]; do n=$((n+1)); done
	if [ -e "$1" ]; then echo live; else echo gone; fi
}
count_lines <(echo one; echo two)`
	if got, want := runProcSubstScript(t, src), "..\ngone\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// The shell answers a stat of its own pipe path from the live registry
// rather than from the filesystem — a named pipe has no filesystem node,
// and a CreateFile on the name is a connection. The answer tracks the
// pipe's life: shaped like one of ours but not served means gone, which is
// what the unlinked FIFO reports on Unix.
func TestProcSubstPipeStatFollowsTheLiveName(t *testing.T) {
	t.Parallel()
	const name = fifoNamePrefix + "5eed1ive"
	for _, path := range []string{
		windowsProcSubstShellDir + name,
		windowsProcSubstNativeDir + name,
	} {
		// Not served: the shell answers for the path, and the answer is
		// that there is no such file.
		info, ok := procSubstPipeStatMode(path, true)
		if !ok {
			t.Fatalf("%q is not recognised as one of our pipes", path)
		}
		if info != nil {
			t.Errorf("%q reported as %v before it was ever served", path, info.Mode())
		}

		procSubstPipeRegister(name)
		info, ok = procSubstPipeStatMode(path, true)
		if !ok || info == nil {
			t.Fatalf("%q: a served pipe must stat, got ok=%v info=%v", path, ok, info)
		}
		if info.Mode()&fs.ModeNamedPipe == 0 || info.Name() != name {
			t.Errorf("%q stats as %v %q", path, info.Mode(), info.Name())
		}

		procSubstPipeRelease(name)
		if info, ok := procSubstPipeStatMode(path, true); !ok || info != nil {
			t.Errorf("%q: a released pipe must be gone, got ok=%v info=%v", path, ok, info)
		}
	}
	// A Unix host has no Windows pipes at all; its FIFOs are real files.
	if _, ok := procSubstPipeStatMode(windowsProcSubstShellDir+name, false); ok {
		t.Error("a Unix host answered for a Windows pipe path")
	}
}

// Nested registrations of one name — a new pipe taking a name an old one is
// still releasing — do not make the live answer flap.
func TestProcSubstPipeLiveIsCounted(t *testing.T) {
	t.Parallel()
	const name = fifoNamePrefix + "c0un7ed"
	procSubstPipeRegister(name)
	procSubstPipeRegister(name)
	procSubstPipeRelease(name)
	if !procSubstPipeIsLive(name) {
		t.Error("the second registration was released by the first release")
	}
	procSubstPipeRelease(name)
	if procSubstPipeIsLive(name) {
		t.Error("still live after every registration was released")
	}
	// Releasing a name nobody holds is not an underflow.
	procSubstPipeRelease(name)
	if procSubstPipeIsLive(name) {
		t.Error("an unheld name came back to life")
	}
}
