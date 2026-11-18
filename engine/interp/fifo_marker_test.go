// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"io/fs"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The marker format is a wire contract with another repo's mkfifo
// (../coreutils/docs/windows-fifo.md, format v1, frozen). These pin the
// bytes, so that a change to them fails here rather than on a Windows host
// where only half the pair was rebuilt. They run everywhere on purpose: the
// format is the part that must not drift, and it needs no named pipes to
// check.

// goldenFifoMarker is a v1 marker written out by hand from the document's
// grammar rather than from the constants under test, so that a typo in the
// constants cannot agree with itself.
const goldenFifoMarker = "!<bashyfifo>\n" +
	"bashy-fifo-9f8c0a1b2d3e4f5061728394a5b6c7d8\n"

const goldenFifoLeaf = "bashy-fifo-9f8c0a1b2d3e4f5061728394a5b6c7d8"

// A v1 marker is exactly 57 bytes: 13 for the magic line, 43 for the leaf,
// 1 for its LF. The document states the number; anything else is a wire
// break needing a version bump.
func TestFifoMarkerSize(t *testing.T) {
	if got := len(goldenFifoMarker); got != 57 {
		t.Fatalf("golden marker is %d bytes, want 57", got)
	}
	if fifoMarkerLen != 57 {
		t.Errorf("fifoMarkerLen = %d, want 57", fifoMarkerLen)
	}
	if fifoLeafLen != 43 {
		t.Errorf("fifoLeafLen = %d, want 43", fifoLeafLen)
	}
	if fifoMarkerMaxLen < fifoMarkerLen {
		t.Errorf("fifoMarkerMaxLen = %d, smaller than a marker", fifoMarkerMaxLen)
	}
}

func TestParseFifoMarkerGolden(t *testing.T) {
	leaf, ok := parseFifoMarker([]byte(goldenFifoMarker))
	if !ok {
		t.Fatalf("golden marker rejected")
	}
	if leaf != goldenFifoLeaf {
		t.Errorf("leaf = %q, want %q", leaf, goldenFifoLeaf)
	}
}

// fifoMarkerBytes must produce exactly what the applet writes.
func TestFifoMarkerBytesRoundTrip(t *testing.T) {
	if got := string(fifoMarkerBytes(goldenFifoLeaf)); got != goldenFifoMarker {
		t.Fatalf("fifoMarkerBytes = %q, want %q", got, goldenFifoMarker)
	}
	leaf, ok := parseFifoMarker(fifoMarkerBytes(goldenFifoLeaf))
	if !ok || leaf != goldenFifoLeaf {
		t.Fatalf("round trip = %q, %v", leaf, ok)
	}
}

// Everything the opener must refuse. Leaf strictness is a security
// boundary: a looser line 2 would let a crafted marker aim an open at an
// arbitrary pipe — \\.\pipe\lsass, a squatted service pipe — so each of
// these is a reject, never a repair.
func TestParseFifoMarkerRejects(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"empty", ""},
		{"magic only", "!<bashyfifo>\n"},
		{"wrong magic", "!<bashyfifo2>\n" + goldenFifoLeaf + "\n"},
		{"future magic", "!<bashyfifo2>\nbashy-fifo-9f8c0a1b2d3e4f5061728394a5b6c7d8\n"},
		{"crlf after magic", "!<bashyfifo>\r\n" + goldenFifoLeaf + "\n"},
		{"crlf after leaf", goldenFifoMarker[:len(goldenFifoMarker)-1] + "\r\n"},
		{"no trailing lf", strings.TrimSuffix(goldenFifoMarker, "\n")},
		{"trailing junk", goldenFifoMarker + "x"},
		{"trailing newline", goldenFifoMarker + "\n"},
		{"leading space", " " + goldenFifoMarker},
		{"uppercase hex", "!<bashyfifo>\nbashy-fifo-9F8C0A1B2D3E4F5061728394A5B6C7D8\n"},
		{"short hex", "!<bashyfifo>\nbashy-fifo-9f8c0a1b2d3e4f5061728394a5b6c7d\n"},
		{"long hex", "!<bashyfifo>\nbashy-fifo-9f8c0a1b2d3e4f5061728394a5b6c7d8a\n"},
		{"non hex", "!<bashyfifo>\nbashy-fifo-9f8c0a1b2d3e4f5061728394a5b6c7dg\n"},
		{"wrong leaf prefix", "!<bashyfifo>\ncygfifo----9f8c0a1b2d3e4f5061728394a5b6c7d8\n"},
		// A traversal attempt kept to the right length, to show the
		// grammar and not the length is what refuses it.
		{"traversal leaf", "!<bashyfifo>\n../../../../../../../../../../../../lsass\n"},
		{"absolute leaf", "!<bashyfifo>\n\\\\.\\pipe\\lsass\\aaaaaaaaaaaaaaaaaaaaaaaaaa\n"},
		{"nul in leaf", "!<bashyfifo>\nbashy-fifo-9f8c0a1b2d3e4f5061728394a5b6c7\x00\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if leaf, ok := parseFifoMarker([]byte(test.content)); ok {
				t.Fatalf("accepted as %q, want reject", leaf)
			}
		})
	}
}

// An opener must be handed the whole file, never a prefix of it, or a
// larger file that merely starts with a marker would become a FIFO.
func TestParseFifoMarkerNeedsWholeFile(t *testing.T) {
	big := goldenFifoMarker + strings.Repeat("x", 64)
	if _, ok := parseFifoMarker([]byte(big)); ok {
		t.Fatal("a file that starts with a marker was accepted")
	}
	if _, ok := parseFifoMarker([]byte(big[:fifoMarkerLen])); !ok {
		t.Fatal("the leading marker bytes alone should still parse")
	}
}

func TestValidFifoLeaf(t *testing.T) {
	if !validFifoLeaf(goldenFifoLeaf) {
		t.Error("golden leaf rejected")
	}
	for _, leaf := range []string{
		"", "bashy-fifo-", "bashy-fifo",
		"bashy-fifo-9f8c0a1b2d3e4f5061728394a5b6c7d", // 31
		"bashy-fifo-9f8c0a1b2d3e4f5061728394a5b6c7d88",
		"bashy-fifo-9f8c0a1b2d3e4f5061728394a5b6c7D8",
		"BASHY-FIFO-9f8c0a1b2d3e4f5061728394a5b6c7d8",
		"bashy-fifo-9f8c0a1b2d3e4f5061728394a5b6c7d/",
	} {
		if validFifoLeaf(leaf) {
			t.Errorf("validFifoLeaf(%q) = true, want false", leaf)
		}
	}
}

// Every leaf this shell draws must be one it would itself accept, and two
// mkfifo calls must never name the same pipe — a FIFO recreated after an
// unlink is a new and distinct one.
func TestNewFifoLeaf(t *testing.T) {
	seen := make(map[string]bool)
	for range 100 {
		leaf := newFifoLeaf()
		if !validFifoLeaf(leaf) {
			t.Fatalf("newFifoLeaf gave an invalid leaf %q", leaf)
		}
		if seen[leaf] {
			t.Fatalf("newFifoLeaf repeated %q", leaf)
		}
		seen[leaf] = true
		if _, ok := parseFifoMarker(fifoMarkerBytes(leaf)); !ok {
			t.Fatalf("marker for %q does not parse", leaf)
		}
	}
}

// The two spellings of a FIFO's pipe, per the document's "Pipe name".
func TestFifoPipePaths(t *testing.T) {
	if got, want := fifoPipePath(goldenFifoLeaf), `\\.\pipe\`+goldenFifoLeaf; got != want {
		t.Errorf("fifoPipePath = %q, want %q", got, want)
	}
	if got, want := fifoPipeShellPath(goldenFifoLeaf), `//./pipe/`+goldenFifoLeaf; got != want {
		t.Errorf("fifoPipeShellPath = %q, want %q", got, want)
	}
}

// A FIFO's pipe must not be mistaken for a process substitution's: the two
// share the \\.\pipe\ namespace but have different lifetimes, and
// procSubstPipeStat answering "no such file" for a FIFO's name would be
// wrong in both directions.
func TestFifoPipeIsNotProcSubstPipe(t *testing.T) {
	for _, path := range []string{
		fifoPipePath(goldenFifoLeaf),
		fifoPipeShellPath(goldenFifoLeaf),
	} {
		if isProcSubstPipePathMode(path, true) {
			t.Errorf("%q looks like a process-substitution pipe", path)
		}
	}
	if strings.HasPrefix(fifoLeafPrefix, fifoNamePrefix) ||
		strings.HasPrefix(fifoNamePrefix, fifoLeafPrefix) {
		t.Errorf("leaf prefix %q and procsubst prefix %q overlap", fifoLeafPrefix, fifoNamePrefix)
	}
}

// statStub is a FileInfo standing in for the marker file's own stat.
type statStub struct {
	name string
	size int64
	mode fs.FileMode
	mod  time.Time
	sys  any
}

func (s statStub) Name() string       { return s.name }
func (s statStub) Size() int64        { return s.size }
func (s statStub) Mode() fs.FileMode  { return s.mode }
func (s statStub) ModTime() time.Time { return s.mod }
func (s statStub) IsDir() bool        { return false }
func (s statStub) Sys() any           { return s.sys }

// A marker stats as a FIFO — that is what `test -p` reads — while keeping
// the marker file's name, mtime and Sys so the timestamp tests still work.
func TestFifoMarkerInfo(t *testing.T) {
	mod := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	sys := new(int)
	info := fifoMarkerInfo{statStub{
		name: "a.pipe",
		size: int64(fifoMarkerLen),
		mode: 0o640,
		mod:  mod,
		sys:  sys,
	}}

	if got := info.Mode(); got&fs.ModeNamedPipe == 0 {
		t.Errorf("Mode = %v, want a named pipe", got)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Errorf("Mode perm = %v, want 0640", got)
	}
	if info.Mode().IsRegular() {
		t.Error("a FIFO must not stat as a regular file")
	}
	if got := info.Size(); got != 0 {
		t.Errorf("Size = %d, want 0 — the marker's own size is not the FIFO's", got)
	}
	if got := info.Name(); got != "a.pipe" {
		t.Errorf("Name = %q, want a.pipe", got)
	}
	if !info.ModTime().Equal(mod) {
		t.Errorf("ModTime = %v, want %v", info.ModTime(), mod)
	}
	if info.Sys() != any(sys) {
		t.Error("Sys was dropped; -nt/-ot/-N read it")
	}
}

// Off Windows the stat hook is the identity: those hosts have real FIFOs.
func TestFifoMarkerStatOtherPlatforms(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows has its own coverage in fifo_marker_windows_test.go")
	}
	info := statStub{name: "plain", size: int64(fifoMarkerLen), mode: 0o644}
	if got := fifoMarkerStat("plain", info); got.Mode() != 0o644 {
		t.Errorf("fifoMarkerStat changed a mode off Windows: %v", got.Mode())
	}
}
