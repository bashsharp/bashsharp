// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

//go:build windows

package interp

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"

	"golang.org/x/sys/windows"
)

// prepareChildFds never returns ExtraFiles on Windows: Go's
// syscall.StartProcess rejects more than the three std files with EWINDOWS,
// so any `exec 3<file` left open would make every later external command
// fail to start. Redirections onto 0-2 already reach the child through
// stdio. When the child is this very binary (a bashy running bashy, or the
// ENOEXEC re-exec) the open descriptors are handed over as inheritable
// handles announced in BASHY_INHERITED_HANDLES; any other program sees
// only stdio, which is also what a Windows-native shell would give it.
//
// BASHY_INHERITED_FDS is deliberately not emitted: its Unix meaning "fd N is
// already open in you" is false here.
func prepareChildFds(r *Runner, execPath string) (*childFds, error) {
	if r == nil || !isSelfExecutable(execPath) {
		return &childFds{}, nil
	}
	return r.windowsHandoffFds()
}

// prepareSelfReexecFds rebuilds the handoff for the ENOEXEC fallback, whose
// target is our own binary whatever the first attempt aimed at. The first
// attempt's duplicates were released after its (failed) Start, so a fresh
// set is needed either way.
func prepareSelfReexecFds(r *Runner, prev *childFds) (*childFds, error) {
	if prev != nil {
		prev.finish()
	}
	if r == nil {
		return &childFds{}, nil
	}
	return r.windowsHandoffFds()
}

var selfExecutable struct {
	once sync.Once
	info os.FileInfo
}

// isSelfExecutable reports whether path names the running binary, by file
// identity rather than spelling: the shell may be reached through a copied
// PATH entry or an 8.3 alias. The self FileInfo is cached; the candidate
// is stat'ed each time, as os.SameFile needs both.
func isSelfExecutable(path string) bool {
	selfExecutable.once.Do(func() {
		exe, err := os.Executable()
		if err != nil {
			return
		}
		selfExecutable.info, _ = os.Stat(exe)
	})
	if selfExecutable.info == nil {
		return false
	}
	candidate, err := os.Stat(path)
	if err != nil {
		return false
	}
	return os.SameFile(candidate, selfExecutable.info)
}

// windowsHandoffFds duplicates every open fd >= 3 as an inheritable handle
// for a child that is this same binary. The duplicates are what the child
// inherits; the parent closes its copies right after Start.
func (r *Runner) windowsHandoffFds() (*childFds, error) {
	var dups []syscall.Handle
	var entries []inheritedHandleEntry
	var bridges []func()
	closeDups := func() {
		for _, h := range dups {
			_ = windows.CloseHandle(windows.Handle(h))
		}
		dups = nil
	}
	fail := func(err error) (*childFds, error) {
		closeDups()
		for _, fn := range bridges {
			fn()
		}
		return nil, err
	}
	for _, h := range selectHandoffFds(r.fdTable, r.fdReadTable, r.fdWriteTable) {
		f := h.file
		if f == nil {
			// A non-file writer (a Go buffer behind `exec 3>&1` of a
			// captured stdout) is bridged through a pipe whose write end
			// is a real handle; the copier drains it into the writer.
			pr, pw, err := os.Pipe()
			if err != nil {
				return fail(err)
			}
			done := make(chan struct{})
			w := h.writer
			go func() {
				_, _ = io.Copy(w, pr)
				_ = pr.Close()
				close(done)
			}()
			f = pw
			bridges = append(bridges, func() {
				_ = pw.Close()
				<-done
			})
		}
		dup, err := duplicateInheritableHandle(f)
		if err != nil && !fileOpen(f) {
			// Closed between selection and now (the table entry outlived
			// its file): not an open descriptor, so nothing to hand over.
			continue
		}
		if err != nil {
			return fail(fmt.Errorf("fd %d: cannot duplicate handle for child: %w", h.fd, err))
		}
		dups = append(dups, dup)
		entries = append(entries, inheritedHandleEntry{
			fd:              h.fd,
			inheritedHandle: inheritedHandle{handle: uintptr(dup), mode: h.mode},
		})
	}
	c := &childFds{
		cleanup: func() {
			for _, fn := range bridges {
				fn()
			}
		},
	}
	if len(entries) == 0 {
		return c, nil
	}
	c.env = []string{BashyInheritedHandlesEnv + "=" + formatInheritedHandles(entries)}
	handles := append([]syscall.Handle(nil), dups...)
	c.sysAttr = func(cmd *exec.Cmd) {
		if cmd.SysProcAttr == nil {
			cmd.SysProcAttr = &syscall.SysProcAttr{}
		}
		cmd.SysProcAttr.AdditionalInheritedHandles = append(cmd.SysProcAttr.AdditionalInheritedHandles, handles...)
	}
	var once sync.Once
	c.afterStart = func() { once.Do(closeDups) }
	return c, nil
}

// duplicateInheritableHandle returns an inheritable duplicate of f's
// handle with the same access. The raw handle is read under
// SyscallConn.Control rather than f.Fd(), which would detach f from the
// runtime poller.
func duplicateInheritableHandle(f *os.File) (syscall.Handle, error) {
	rc, err := f.SyscallConn()
	if err != nil {
		return 0, err
	}
	var dup windows.Handle
	var dupErr error
	if err := rc.Control(func(fd uintptr) {
		self := windows.CurrentProcess()
		dupErr = windows.DuplicateHandle(self, windows.Handle(fd), self, &dup, 0, true, windows.DUPLICATE_SAME_ACCESS)
	}); err != nil {
		return 0, err
	}
	if dupErr != nil {
		return 0, dupErr
	}
	return syscall.Handle(dup), nil
}

// adoptInheritedHandles consumes the BASHY_INHERITED_HANDLES a parent bashy
// set for us: each entry becomes a registered inherited fd, materialised
// lazily by inheritedFd on first use like the Unix WithInheritedFds set.
// Entries whose handle is not open in this process are dropped: the
// variable is filtered from every environment we hand out, but a foreign
// intermediary could still have relayed a stale one.
func (r *Runner) adoptInheritedHandles(spec string) {
	handles, err := parseInheritedHandles(spec)
	if err != nil || len(handles) == 0 {
		return
	}
	for fd, h := range handles {
		if _, err := windows.GetFileType(windows.Handle(h.handle)); err != nil {
			delete(handles, fd)
			continue
		}
		if r.inheritedFds == nil {
			r.inheritedFds = make(map[int]bool)
		}
		r.inheritedFds[fd] = true
	}
	r.inheritedHandles = handles
}
