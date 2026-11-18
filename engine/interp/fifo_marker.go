// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"crypto/rand"
	"encoding/hex"
	"io/fs"
	"strings"
)

// Windows FIFOs: the marker file half of format v1, as frozen in the
// coreutils repo's docs/windows-fifo.md. `mkfifo` writes the marker; this
// shell is the opener, and every open of a path carrying one must connect
// to the named pipe it names instead of reading the file.
//
// A Windows named pipe lives in the machine-local \\.\pipe\ namespace, not
// on a filesystem, so a path like C:\Temp\a.pipe cannot itself be a pipe.
// The marker is the only durable artifact: a regular file with
// FILE_ATTRIBUTE_SYSTEM set whose two LF-terminated ASCII lines are the
// format's magic and the leaf of the pipe to rendezvous on.
//
//	!<bashyfifo>\n
//	bashy-fifo-<32 lowercase hex>\n
//
// The code below is deliberately platform-neutral so that the format — the
// one part of this that is a wire contract with another repo — can be
// proven on any host. The rendezvous itself is in fifo_marker_windows.go.
//
// Any change to these bytes is a wire break and needs a version bump in the
// magic line; do not relax the grammar. Leaf strictness is a security
// boundary: an opener that accepted a looser second line could be pointed
// by a crafted marker at an arbitrary pipe (\\.\pipe\lsass, a squatted
// service pipe). Reject; never sanitise.
const (
	// fifoMarkerMagic is line 1 of a v1 marker, without its LF.
	fifoMarkerMagic = "!<bashyfifo>"
	// fifoLeafPrefix begins line 2, the pipe leaf.
	fifoLeafPrefix = "bashy-fifo-"
	// fifoLeafHexLen is how many lowercase hex digits follow the prefix:
	// 128 random bits from a CSPRNG, chosen at mkfifo time.
	fifoLeafHexLen = 32

	// fifoLeafLen is the exact length of a v1 leaf, 43 bytes.
	fifoLeafLen = len(fifoLeafPrefix) + fifoLeafHexLen
	// fifoMarkerLen is the exact size of a v1 marker, 57 bytes:
	// 13 (magic + LF) + 43 (leaf) + 1 (LF). Nothing follows line 2, and
	// CRLF anywhere is a mismatch — both fall out of the exact length.
	fifoMarkerLen = len(fifoMarkerMagic) + 1 + fifoLeafLen + 1
	// fifoMarkerMaxLen is the size at which a file stops being a candidate
	// marker at all, per the detection rules. It is larger than
	// fifoMarkerLen so that a future format with a longer line 2 is still
	// read (and then rejected by its magic) rather than silently ignored.
	fifoMarkerMaxLen = 128
)

// The two spellings of a FIFO's pipe. CreateNamedPipe and CreateFile are
// given the documented native form; the forward-slash form is the one that
// survives shell word re-parsing, and is the same convention process
// substitution uses (see windowsProcSubstNativeDir).
const (
	fifoPipeNativeDir = `\\.\pipe\`
	fifoPipeShellDir  = `//./pipe/`
)

// parseFifoMarker returns the pipe leaf named by a candidate marker's full
// content. ok is false unless the bytes match the v1 grammar exactly:
//
//	marker = %s"!<bashyfifo>" LF leaf LF
//	leaf   = %s"bashy-fifo-" 32(%x30-39 / %x61-66)
//
// Callers must pass the whole file, not a prefix of it: a marker is exactly
// fifoMarkerLen bytes and anything longer is some other file.
func parseFifoMarker(content []byte) (leaf string, ok bool) {
	if len(content) != fifoMarkerLen {
		return "", false
	}
	rest, ok := strings.CutPrefix(string(content), fifoMarkerMagic+"\n")
	if !ok {
		return "", false
	}
	leaf, ok = strings.CutSuffix(rest, "\n")
	if !ok {
		return "", false
	}
	if !validFifoLeaf(leaf) {
		return "", false
	}
	return leaf, true
}

// validFifoLeaf reports whether leaf is a well-formed v1 pipe leaf. Only
// lowercase hex counts; uppercase, a short or long tail, or anything that
// could climb out of the pipe namespace is rejected rather than repaired.
func validFifoLeaf(leaf string) bool {
	hexDigits, ok := strings.CutPrefix(leaf, fifoLeafPrefix)
	if !ok || len(hexDigits) != fifoLeafHexLen {
		return false
	}
	for i := range len(hexDigits) {
		c := hexDigits[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// fifoMarkerBytes is the v1 marker naming leaf, which must be valid.
func fifoMarkerBytes(leaf string) []byte {
	return []byte(fifoMarkerMagic + "\n" + leaf + "\n")
}

// newFifoLeaf draws a fresh leaf: 128 random bits from the CSPRNG, so that
// a FIFO recreated after an unlink is a new and distinct one.
func newFifoLeaf() string { return fifoLeafPrefix + randomFifoHex() }

// randomFifoHex is the 32 lowercase hex digits a leaf ends in: 128 bits
// from the CSPRNG. The read-write loopback pipe names itself from this too,
// where unguessability is what keeps another local user from squatting the
// name in the window before this process connects to it.
func randomFifoHex() string {
	var buf [fifoLeafHexLen / 2]byte
	// crypto/rand.Read has not failed since Go 1.24; it panics instead.
	rand.Read(buf[:])
	return hex.EncodeToString(buf[:])
}

// fifoPipePath is the native path CreateNamedPipe and CreateFile take for a
// FIFO's leaf. Named pipes are machine-local: a marker on a network share
// does not rendezvous across machines.
func fifoPipePath(leaf string) string { return fifoPipeNativeDir + leaf }

// fifoPipeShellPath is the forward-slash spelling of the same pipe, for
// anywhere the name has to survive being re-read as a shell word.
func fifoPipeShellPath(leaf string) string { return fifoPipeShellDir + leaf }

// fifoMarkerInfo is how a path carrying a marker stats: a FIFO. Reporting
// the type is what makes `test -p`, and the `! -f` half of the suite's FIFO
// probes, agree with the fact that opening the path rendezvouses on a pipe.
//
// The embedded info keeps the marker file's name, mtime and Sys, so the
// timestamp tests (-nt, -ot, -N) still read the on-disk values; only the
// type, and the size a FIFO does not have, are the pipe's.
type fifoMarkerInfo struct{ fs.FileInfo }

func (fifoMarkerInfo) Size() int64 { return 0 }

func (i fifoMarkerInfo) Mode() fs.FileMode {
	return fs.ModeNamedPipe | i.FileInfo.Mode().Perm()
}
