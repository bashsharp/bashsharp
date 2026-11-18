// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
)

// childFds is the platform's contribution to an external command's file
// descriptor picture beyond stdio. prepareChildFds fills it; the exec
// handler threads the pieces into the exec.Cmd it builds.
//
// On Unix extraFiles pads fds 3..max into exec.Cmd.ExtraFiles and env
// carries BASHY_INHERITED_FDS. Windows cannot pass ExtraFiles at all (Go's
// syscall.StartProcess rejects more than three std files), so extraFiles
// stays nil there; when the child is this very binary the open descriptors
// travel as inheritable handles instead (sysAttr + BASHY_INHERITED_HANDLES).
type childFds struct {
	extraFiles []*os.File
	// env holds complete NAME=VALUE entries to append to the child's
	// environment; empty when the platform has nothing to announce.
	env []string
	// sysAttr, when non-nil, amends the exec.Cmd's SysProcAttr with the
	// handles the child must inherit.
	sysAttr func(*exec.Cmd)
	// afterStart releases what only the Start needed (the parent's
	// inheritable duplicates); safe to call more than once.
	afterStart func()
	// cleanup releases everything else once the command is done: pipe
	// bridges for non-file writers and their copier goroutines.
	cleanup func()
}

func (c *childFds) started() {
	if c.afterStart != nil {
		c.afterStart()
	}
}

func (c *childFds) finish() {
	c.started()
	if c.cleanup != nil {
		c.cleanup()
	}
}

// handoffFd is one numbered descriptor an external child may receive.
// Exactly one of file and writer is set: writer is a non-*os.File sink from
// fdWriteTable that must be bridged through a pipe before it can be handed
// to another process.
type handoffFd struct {
	fd     int
	mode   string // "r", "w" or "rw"
	file   *os.File
	writer io.Writer
}

// fileOpen reports whether f still has a live descriptor. A table entry
// can outlive its file: `{ exec 10>&1; } >log` leaves fd 10 pointing at
// the block's stdout after the block closed it, and duplicating that
// handle for a child fails with "use of closed file". The probe is the
// same Control call the duplication makes, so it errs exactly when that
// would.
func fileOpen(f *os.File) bool {
	if f == nil {
		return false
	}
	rc, err := f.SyscallConn()
	if err != nil {
		return false
	}
	return rc.Control(func(uintptr) {}) == nil
}

// selectHandoffFds lists the descriptors >= 3 the runner holds open, in
// ascending fd order, skipping holes. fdTable, fdReadTable and fdWriteTable
// are the runner's own maps; the mode is derived from which side tables
// mention the fd, defaulting to read-only for a bare fdTable entry. A
// closed *os.File in either table is not an open descriptor: it is skipped
// (see fileOpen) rather than failing the exec.
func selectHandoffFds(fdTable map[int]*os.File, fdReadTable map[int]bool, fdWriteTable map[int]io.Writer) []handoffFd {
	seen := make(map[int]bool, len(fdTable)+len(fdWriteTable))
	var fds []int
	for fd, f := range fdTable {
		if fd >= 3 && fileOpen(f) && !seen[fd] {
			seen[fd] = true
			fds = append(fds, fd)
		}
	}
	for fd, w := range fdWriteTable {
		if fd < 3 || w == nil || seen[fd] {
			continue
		}
		if f, ok := w.(*os.File); ok && !fileOpen(f) {
			continue
		}
		seen[fd] = true
		fds = append(fds, fd)
	}
	slices.Sort(fds)
	out := make([]handoffFd, 0, len(fds))
	for _, fd := range fds {
		h := handoffFd{fd: fd}
		read := fdReadTable[fd]
		write := fdWriteTable[fd] != nil
		switch {
		case read && write:
			h.mode = "rw"
		case write:
			h.mode = "w"
		default:
			h.mode = "r"
		}
		if f := fdTable[fd]; fileOpen(f) {
			h.file = f
		} else if f, ok := fdWriteTable[fd].(*os.File); ok {
			h.file = f
		} else {
			h.writer = fdWriteTable[fd]
		}
		out = append(out, h)
	}
	return out
}

// inheritedHandle is one entry of BASHY_INHERITED_HANDLES as seen by the
// child: the raw OS handle value and the access the parent granted it.
type inheritedHandle struct {
	handle uintptr
	mode   string // "r", "w" or "rw"
}

// inheritedHandleEntry is the parent-side view: which shell fd the handle
// stands for.
type inheritedHandleEntry struct {
	fd int
	inheritedHandle
}

// formatInheritedHandles renders entries as the BASHY_INHERITED_HANDLES
// value: <fd>:<r|w|rw>:0x<handle>, comma-separated, in the order given.
func formatInheritedHandles(entries []inheritedHandleEntry) string {
	parts := make([]string, 0, len(entries))
	for _, e := range entries {
		parts = append(parts, strconv.Itoa(e.fd)+":"+e.mode+":0x"+strconv.FormatUint(uint64(e.handle), 16))
	}
	return strings.Join(parts, ",")
}

// parseInheritedHandles decodes a BASHY_INHERITED_HANDLES value. An empty
// spec yields an empty map. Any malformed entry rejects the whole value:
// the variable is private to bashy, so a bad spelling means corruption
// rather than user intent.
func parseInheritedHandles(spec string) (map[int]inheritedHandle, error) {
	out := make(map[int]inheritedHandle)
	if spec == "" {
		return out, nil
	}
	for _, part := range strings.Split(spec, ",") {
		fields := strings.Split(part, ":")
		if len(fields) != 3 {
			return nil, fmt.Errorf("inherited handle %q: want fd:mode:0xhandle", part)
		}
		fd, err := strconv.Atoi(fields[0])
		if err != nil || fd < 3 {
			return nil, fmt.Errorf("inherited handle %q: bad fd", part)
		}
		switch fields[1] {
		case "r", "w", "rw":
		default:
			return nil, fmt.Errorf("inherited handle %q: bad mode", part)
		}
		hex, ok := strings.CutPrefix(fields[2], "0x")
		if !ok || hex == "" {
			return nil, fmt.Errorf("inherited handle %q: bad handle", part)
		}
		h, err := strconv.ParseUint(hex, 16, 64)
		if err != nil || h == 0 || h > uint64(^uintptr(0)) {
			return nil, fmt.Errorf("inherited handle %q: bad handle", part)
		}
		if _, dup := out[fd]; dup {
			return nil, fmt.Errorf("inherited handle %q: duplicate fd", part)
		}
		out[fd] = inheritedHandle{handle: uintptr(h), mode: fields[1]}
	}
	return out, nil
}

// bindInheritedFile registers f as the runner's fd, readable and/or
// writable per mode, the way os_unix.go's inheritedFd does after F_GETFL.
func (r *Runner) bindInheritedFile(fd int, f *os.File, mode string) {
	if r.fdTable == nil {
		r.fdTable = make(map[int]*os.File)
	}
	r.fdTable[fd] = f
	if strings.Contains(mode, "r") {
		if r.fdReadTable == nil {
			r.fdReadTable = make(map[int]bool)
		}
		r.fdReadTable[fd] = true
	}
	if strings.Contains(mode, "w") {
		if r.fdWriteTable == nil {
			r.fdWriteTable = make(map[int]io.Writer)
		}
		r.fdWriteTable[fd] = f
	}
}
