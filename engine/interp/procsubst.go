// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp

import (
	"io/fs"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
)

// procSubstPipe is the platform seam behind process substitution: a
// filesystem-visible rendezvous path whose other end the shell (or a child
// process) opens by name. On Unix it is a FIFO in the runner's temp dir; on
// Windows it is a \\.\pipe\ named pipe. Both open calls block until the
// consumer side opens the path, matching Bash's FIFO semantics.
type procSubstPipe interface {
	// path is the name substituted into the command line.
	path() string
	// openWriter opens the substitution's write end (`<(cmd)`).
	openWriter() (*os.File, error)
	// openReader opens the substitution's read end (`>(cmd)`).
	openReader() (*os.File, error)
	// cleanup releases the rendezvous once the substitution is done. It is
	// safe to call after the opened end has been closed.
	cleanup()
}

// Windows process-substitution pipes have two spellings. CreateNamedPipe is
// given the documented native form, \\.\pipe\sh-np-<hex>. The command line
// gets //./pipe/sh-np-<hex>: the same local-device path (Win32 path
// normalisation accepts either separator before the \\.\ check, and Go's
// os.OpenFile passes a short //./ path to CreateFile untouched), but one
// that survives being re-read by the shell — `eval cat <(echo x)` used to
// hand cat `\.pipesh-np-…` once the backslashes had served as escapes.
// pathconv.ToOSMountsMode passes a leading // through unchanged, so the
// mount table never prefixes it with a drive.
const (
	windowsProcSubstNativeDir = `\\.\pipe\`
	windowsProcSubstShellDir  = `//./pipe/`
)

// windowsProcSubstPipeNames returns the native (CreateNamedPipe) and shell
// (command-line) spellings of the pipe for one random suffix.
func windowsProcSubstPipeNames(suffix string) (native, shell string) {
	return windowsProcSubstNativeDir + fifoNamePrefix + suffix,
		windowsProcSubstShellDir + fifoNamePrefix + suffix
}

// isProcSubstPipePath reports whether path names one of this shell's
// Windows process-substitution pipes, in either spelling. Only meaningful
// in windows mode; every other host has its FIFOs in the temp dir.
func isProcSubstPipePath(path string) bool {
	return isProcSubstPipePathMode(path, runtime.GOOS == "windows")
}

// isProcSubstPipePathMode is [isProcSubstPipePath] with an explicit windows
// flag.
func isProcSubstPipePathMode(path string, windows bool) bool {
	if !windows {
		return false
	}
	for _, dir := range [...]string{windowsProcSubstShellDir, windowsProcSubstNativeDir} {
		if rest, ok := strings.CutPrefix(path, dir); ok {
			return strings.HasPrefix(rest, fifoNamePrefix) && !strings.ContainsAny(rest, `/\`)
		}
	}
	return false
}

// procSubstPipeLive records the names of the process-substitution pipes
// this process is currently serving. A Windows named pipe is not a
// filesystem node, so the shell answers its own stat of the path from this
// registry rather than from the filesystem (see [procSubstPipeStat]); the
// registry is what makes that answer track the pipe's life the way a FIFO's
// does on Unix, where [procSubstFIFO.cleanup] unlinks the path and a later
// `test -e` of it is false.
//
// Keyed by the pipe's basename, which is unique to one substitution; the
// count only guards the release of a name a new pipe has already taken.
var procSubstPipeLive struct {
	mu    sync.Mutex
	names map[string]int
}

// procSubstPipeRegister marks name as a live process-substitution pipe.
func procSubstPipeRegister(name string) {
	procSubstPipeLive.mu.Lock()
	defer procSubstPipeLive.mu.Unlock()
	if procSubstPipeLive.names == nil {
		procSubstPipeLive.names = make(map[string]int)
	}
	procSubstPipeLive.names[name]++
}

// procSubstPipeRelease undoes one [procSubstPipeRegister]. Once the last
// registration of a name is released, the shell reports the path as gone,
// as the unlinked FIFO of the same substitution is on Unix.
func procSubstPipeRelease(name string) {
	procSubstPipeLive.mu.Lock()
	defer procSubstPipeLive.mu.Unlock()
	if n := procSubstPipeLive.names[name]; n > 1 {
		procSubstPipeLive.names[name] = n - 1
	} else {
		delete(procSubstPipeLive.names, name)
	}
}

// procSubstPipeIsLive reports whether name is a pipe the shell still serves.
func procSubstPipeIsLive(name string) bool {
	procSubstPipeLive.mu.Lock()
	defer procSubstPipeLive.mu.Unlock()
	return procSubstPipeLive.names[name] > 0
}

// procSubstPipeInfo is the synthetic stat result for a Windows
// process-substitution pipe. A named pipe has no filesystem node: every
// CreateFile on \\.\pipe\<name> — os.Stat's included — connects a client to
// a server instance, which for the live stream would consume the rendezvous
// meant for the real consumer. The shell therefore answers its own
// `test -e`/`-p`/`[[ -e ]]` of the path without touching the pipe, the way
// stat() of a FIFO on Unix does not open it.
type procSubstPipeInfo struct{ name string }

func (i procSubstPipeInfo) Name() string     { return i.name }
func (procSubstPipeInfo) Size() int64        { return 0 }
func (procSubstPipeInfo) Mode() fs.FileMode  { return fs.ModeNamedPipe | 0o600 }
func (procSubstPipeInfo) ModTime() time.Time { return time.Time{} }
func (procSubstPipeInfo) IsDir() bool        { return false }
func (procSubstPipeInfo) Sys() any           { return nil }

// procSubstPipeStat reports how the shell answers a stat of path itself.
//
// ok is false when path is not one of this shell's process-substitution
// pipes; the caller goes to the filesystem. ok is true with a nil info when
// path is shaped like one but the shell no longer serves it: the answer is
// then "no such file", exactly what a stat of the FIFO of a finished
// substitution gives on Unix, where cleanup unlinked it. That is what lets
// a script tell a live substitution from a spent one — `[ -e "$1" ]` of a
// //./pipe/sh-np-* path is true only while there is something to read.
func procSubstPipeStat(path string) (fs.FileInfo, bool) {
	return procSubstPipeStatMode(path, runtime.GOOS == "windows")
}

// procSubstPipeStatMode is [procSubstPipeStat] with an explicit windows flag.
func procSubstPipeStatMode(path string, windows bool) (fs.FileInfo, bool) {
	if !isProcSubstPipePathMode(path, windows) {
		return nil, false
	}
	name := procSubstPipeName(path)
	if !procSubstPipeIsLive(name) {
		return nil, true
	}
	return procSubstPipeInfo{name: name}, true
}

// procSubstPipeName is the basename of a pipe path in either spelling, which
// is the key the live registry uses.
func procSubstPipeName(path string) string {
	if i := strings.LastIndexAny(path, `/\`); i >= 0 {
		return path[i+1:]
	}
	return path
}

// procSubstPipeStatErr is [procSubstPipeStat] for callers that want the
// "no such file" answer as an error.
func procSubstPipeStatErr(op, path string) (fs.FileInfo, error, bool) {
	info, ok := procSubstPipeStat(path)
	if !ok {
		return nil, nil, false
	}
	if info == nil {
		return nil, &fs.PathError{Op: op, Path: path, Err: fs.ErrNotExist}, true
	}
	return info, nil, true
}
