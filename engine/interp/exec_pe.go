// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
)

// An extensionless PE image — the shell copied to $TMPDIR/sh by
// posixexp.tests, or a Cygwin-style twin of an .exe — is something
// CreateProcess runs by full path whatever its name, but os/exec's Start
// refuses it: lookExtensions accepts only a file carrying a PATHEXT
// suffix, and reports "executable file not found in %PATH%". The exec
// therefore goes through a name os/exec does accept:
//
//   - a file that is this very binary, copied (the same size and the same
//     first 4 KiB), runs as os.Executable(), which also lets the child be
//     recognised as self for the descriptor handoff;
//   - any other image runs through a hard link <TEMP>\bashy-exec-<rand>.exe
//     (or one beside the file when TEMP is another volume), or a copy as
//     the last resort, removed once the command has been waited for.
//
// argv[0] is untouched either way, so $0 in the child stays /tmp/sh. The
// decision logic is host-independent; exec_prepare_windows.go wires it in.

// peImageCompareSize is how much of two images sameExecutableImage
// compares beyond their size: the DOS/PE headers and section table of two
// different programs differ well within it.
const peImageCompareSize = 4096

// isPEImage reports whether the probe of a file starts with the DOS header
// magic every PE image carries.
func isPEImage(probe []byte) bool {
	return len(probe) >= 2 && probe[0] == 'M' && probe[1] == 'Z'
}

// sameExecutableImage reports whether a and b hold the same program image:
// the same size and the same first peImageCompareSize bytes.
func sameExecutableImage(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ia, err := os.Stat(a)
	if err != nil {
		return false
	}
	ib, err := os.Stat(b)
	if err != nil {
		return false
	}
	if os.SameFile(ia, ib) {
		return true
	}
	if ia.Size() != ib.Size() || !ia.Mode().IsRegular() || !ib.Mode().IsRegular() {
		return false
	}
	ha, err := readImageHead(a)
	if err != nil {
		return false
	}
	hb, err := readImageHead(b)
	if err != nil {
		return false
	}
	return bytes.Equal(ha, hb)
}

func readImageHead(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, peImageCompareSize)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, err
	}
	return buf[:n], nil
}

// planExtensionlessPEExec decides how to run execPath, whose probe is the
// start of its contents, when it is an extensionless PE image: as self
// when it is a copy of the running binary self, else through an alias
// under tempDir (see makeExecAlias). ok is false when execPath is not an
// extensionless PE image, or no alias could be made; cleanup, when
// non-nil, removes the alias and is to be called after the wait.
func planExtensionlessPEExec(execPath string, probe []byte, self, tempDir string) (path string, cleanup func(), ok bool) {
	if winHasExt(execPath) || !isPEImage(probe) {
		return "", nil, false
	}
	if sameExecutableImage(execPath, self) {
		return self, nil, true
	}
	alias, remove, err := makeExecAlias(execPath, tempDir)
	if err != nil {
		return "", nil, false
	}
	return alias, remove, true
}

// makeExecAlias gives target a name os/exec accepts: a hard link
// bashy-exec-<rand>.exe under tempDir, or beside target when tempDir is on
// another volume, or a copy under tempDir when neither link can be made.
// remove deletes the alias.
func makeExecAlias(target, tempDir string) (alias string, remove func(), err error) {
	for _, dir := range []string{tempDir, filepath.Dir(target)} {
		if dir == "" {
			continue
		}
		name, err := reserveExecAlias(dir)
		if err != nil {
			continue
		}
		if err := linkExecAlias(target, name); err == nil {
			return name, func() { _ = os.Remove(name) }, nil
		}
	}
	f, err := os.CreateTemp(tempDir, "bashy-exec-*.exe")
	if err != nil {
		return "", nil, err
	}
	name := f.Name()
	src, err := os.Open(target)
	if err == nil {
		_, err = io.Copy(f, src)
		src.Close()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(name)
		return "", nil, err
	}
	if err := os.Chmod(name, 0o755); err != nil {
		_ = os.Remove(name)
		return "", nil, err
	}
	return name, func() { _ = os.Remove(name) }, nil
}

// linkExecAlias is os.Link; tests pin it to force the copy fallback.
var linkExecAlias = os.Link

// reserveExecAlias picks a fresh bashy-exec-<rand>.exe name in dir for a
// hard link to take: CreateTemp guarantees the uniqueness, the file itself
// is not wanted.
func reserveExecAlias(dir string) (string, error) {
	f, err := os.CreateTemp(dir, "bashy-exec-*.exe")
	if err != nil {
		return "", err
	}
	name := f.Name()
	f.Close()
	if err := os.Remove(name); err != nil {
		return "", err
	}
	return name, nil
}
