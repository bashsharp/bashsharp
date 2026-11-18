// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

//go:build windows

package interp

import (
	"context"
	"io"
	"io/fs"
	"os"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The opener half of the Windows FIFO contract (fifo_marker.go has the
// format, ../coreutils/docs/windows-fifo.md is the frozen document). A
// path whose file is a v1 marker is a FIFO: opening it rendezvouses on the
// named pipe the marker names, and stat'ing it reports type p.
//
// The model is Cygwin's, and the document's: readers own pipe instances
// (servers), writers are clients. `mkfifo` creates no pipe — an instance
// dies with the process that made it, and the FIFO has to outlive mkfifo —
// so the first reader to open the path is what brings the pipe into
// existence, and a writer that finds no instance yet retries until one
// appears. That is POSIX's "open blocks until a peer arrives", built out of
// the two errors Windows gives instead of blocking.
//
// # Why this is not procsubst_windows.go
//
// Process substitution serves one pipe for the life of one command, holds
// the server end itself, and answers a second open with end-of-stream. A
// FIFO is the opposite on every count: its name is on disk and outlives the
// shell, every open is a fresh rendezvous, and there is no registry of live
// names to answer a stat from — the marker file is the registry, and the
// filesystem keeps it. What the two do share is the mechanics of a blocking
// ConnectNamedPipe that has to be callable off (connectInstance, and the
// client-connect nudge that releases it), so those are reused rather than
// rewritten.
//
// # Lifetime
//
// Handles are the pipe's, never the marker's: the marker is opened only to
// be read, and closed again before the rendezvous starts. `rm -f a.pipe`
// therefore deletes it while an `exec 9<> a.pipe` fd stays live and usable
// — which is exactly what read2.sub does before running 2000 iterations
// through fd 9 — and it needs no share-delete cooperation from us.

var procWaitNamedPipeW = modkernel32.NewProc("WaitNamedPipeW")

const (
	// fifoPipeBuf is the kernel buffer each instance asks for, per the
	// document's CreateNamedPipe call.
	fifoPipeBuf = 65536

	// fifoOpenRetry is how long a writer waits before trying again when no
	// reader is listening yet, standing in for the block POSIX would do.
	fifoOpenRetry = 10 * time.Millisecond
	// fifoBusyWaitMs is the WaitNamedPipe timeout used when readers exist
	// but every instance is taken.
	fifoBusyWaitMs = 50
)

// fifoMarkerLeaf reports the pipe leaf of the FIFO marker at path.
//
// ok is false when path is not a FIFO, in which case the caller treats it
// as the ordinary file it is — all three of the document's detection rules
// must hold, and a file that satisfies some of them is never "almost a
// FIFO". err is non-nil only when path passed the cheap attribute test but
// could not then be read: the marker has to be read even for a write-only
// open, so a mode that denies this process read denies it every open. That
// is the one divergence from POSIX FIFO permissions the document records,
// and reporting it beats silently writing over a marker's bytes.
func fifoMarkerLeaf(path string) (leaf string, ok bool, err error) {
	if !windowsShareDeleteEligible(path) {
		// A device, a \\.\ or \\?\ namespace path, or a name long enough
		// to need Go's rewriting: none of them is a marker file.
		return "", false, nil
	}
	name16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", false, nil
	}
	var meta windows.Win32FileAttributeData
	if err := windows.GetFileAttributesEx(name16, windows.GetFileExInfoStandard,
		(*byte)(unsafe.Pointer(&meta))); err != nil {
		// Missing, or unreadable metadata; the caller's own open reports
		// it with the wording it always had.
		return "", false, nil
	}
	// Both cheap rules first, so that the read below — the only part that
	// can deny an open that would otherwise have worked — is reached only
	// by a file small enough and marked enough to really be a marker.
	if !fifoMarkerAttrs(meta.FileAttributes) ||
		meta.FileSizeHigh != 0 || meta.FileSizeLow > fifoMarkerMaxLen {
		return "", false, nil
	}
	// FILE_SHARE_DELETE so that reading a marker cannot make a concurrent
	// `rm` of it fail, and FILE_FLAG_OPEN_REPARSE_POINT is deliberately
	// not set: a symlink to a FIFO is a FIFO, as on POSIX.
	h, err := windows.CreateFile(name16, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		if err == windows.ERROR_ACCESS_DENIED {
			return "", false, &os.PathError{Op: "open", Path: path, Err: syscall.EACCES}
		}
		return "", false, nil
	}
	defer windows.CloseHandle(h)
	content, ok := readFifoMarker(h)
	if !ok {
		return "", false, nil
	}
	leaf, ok = parseFifoMarker(content)
	return leaf, ok, nil
}

// fifoMarkerAttrs applies the attribute half of detection: a marker is a
// regular file with FILE_ATTRIBUTE_SYSTEM set. The attribute is Cygwin's
// convention for special files, and requiring it is what keeps magic bytes
// alone from turning a user's ordinary file into a FIFO.
func fifoMarkerAttrs(attrs uint32) bool {
	if attrs&windows.FILE_ATTRIBUTE_SYSTEM == 0 {
		return false
	}
	return attrs&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_DEVICE) == 0
}

// readFifoMarker reads a candidate marker whole. ok is false if the file is
// larger than a marker may be, which is the document's size rule and also
// what keeps this off the hot path for ordinary files.
func readFifoMarker(h windows.Handle) (content []byte, ok bool) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return nil, false
	}
	if info.FileSizeHigh != 0 || info.FileSizeLow > fifoMarkerMaxLen {
		return nil, false
	}
	buf := make([]byte, info.FileSizeLow)
	for off := uint32(0); off < info.FileSizeLow; {
		var n uint32
		if err := windows.ReadFile(h, buf[off:], &n, nil); err != nil {
			return nil, false
		}
		if n == 0 {
			return nil, false
		}
		off += n
	}
	return buf, true
}

// openFifoMarker performs an open of a FIFO whose marker names leaf. The
// three access modes are the three halves-of-a-rendezvous POSIX defines,
// and each blocks the way its POSIX counterpart does; ctx cancellation ends
// the wait, as it does for the Unix FIFO open.
func openFifoMarker(ctx context.Context, path, leaf string, flag int) (io.ReadWriteCloser, error) {
	native := fifoPipePath(leaf)
	switch flag & (os.O_WRONLY | os.O_RDWR) {
	case 0:
		// O_CREAT/O_TRUNC/O_EXCL are meaningless against an existing FIFO
		// and are simply not consulted here: truncating a pipe is a no-op
		// on POSIX, and `> fifo` must not overwrite the marker's bytes.
		return fifoOpenRead(ctx, path, native)
	case os.O_WRONLY:
		return fifoOpenWrite(ctx, path, native)
	case os.O_RDWR:
		return fifoOpenReadWrite(ctx, path, native)
	}
	return nil, &os.PathError{Op: "open", Path: path, Err: syscall.EINVAL}
}

// createFifoInstance creates one server instance of a FIFO's pipe, with the
// exact CreateNamedPipe arguments the document fixes. Every instance of a
// name must agree on mode and type, so mkfifo's absence of a pipe is not a
// problem: whichever reader arrives first creates the first instance, and
// FILE_FLAG_FIRST_PIPE_INSTANCE is deliberately not set so that concurrent
// readers are legal.
func createFifoInstance(native string) (windows.Handle, error) {
	name16, err := windows.UTF16PtrFromString(native)
	if err != nil {
		return windows.InvalidHandle, err
	}
	h, err := windows.CreateNamedPipe(name16, windows.PIPE_ACCESS_INBOUND,
		windows.PIPE_TYPE_BYTE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS,
		windows.PIPE_UNLIMITED_INSTANCES, fifoPipeBuf, fifoPipeBuf, 0, nil)
	if err == windows.ERROR_ACCESS_DENIED {
		// The leaf exists as another user's pipe.
		err = syscall.EACCES
	}
	return h, err
}

// fifoOpenRead is O_RDONLY: create an instance and block in
// ConnectNamedPipe until a writer connects, which is POSIX's
// open-blocks-until-a-writer. Reading then proceeds on the instance, and
// ERROR_BROKEN_PIPE on read — which Go reports as EOF — is the writer
// having closed. v1 ends the stream there: one writer connection per open.
func fifoOpenRead(ctx context.Context, path, native string) (io.ReadWriteCloser, error) {
	h, err := createFifoInstance(native)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	done := fifoConnectAsync(h)
	select {
	case err := <-done:
		if err != nil {
			windows.CloseHandle(h)
			return nil, &os.PathError{Op: "open", Path: path, Err: err}
		}
		return os.NewFile(uintptr(h), path), nil
	case <-ctx.Done():
		go fifoAbandonInstance(h, native, done)
		return nil, &os.PathError{Op: "open", Path: path, Err: ctx.Err()}
	}
}

// fifoOpenWrite is O_WRONLY: a plain client CreateFile on the pipe, retried
// while no reader is listening. ERROR_FILE_NOT_FOUND is the whole of "no
// reader yet" — the pipe object does not exist until a reader creates its
// first instance — and ERROR_PIPE_BUSY is readers existing with every
// instance taken.
func fifoOpenWrite(ctx context.Context, path, native string) (io.ReadWriteCloser, error) {
	h, err := fifoDial(ctx, native, windows.GENERIC_WRITE, true)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}

// fifoDial connects one client to the pipe. With wait set it keeps trying
// until a reader shows up or ctx is done; without it, a name with no
// listening instance fails at once (which is what the O_NONBLOCK writer
// would want, and what the read-write self-connect needs so it does not sit
// waiting for a pipe it is itself about to serve).
func fifoDial(ctx context.Context, native string, access uint32, wait bool) (windows.Handle, error) {
	name16, err := windows.UTF16PtrFromString(native)
	if err != nil {
		return windows.InvalidHandle, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return windows.InvalidHandle, err
		}
		h, err := windows.CreateFile(name16, access, 0, nil,
			windows.OPEN_EXISTING, 0, 0)
		if err == nil {
			return h, nil
		}
		switch err {
		case windows.ERROR_FILE_NOT_FOUND, windows.ERROR_PATH_NOT_FOUND:
			if !wait {
				// POSIX's O_NONBLOCK writer with no reader.
				return windows.InvalidHandle, syscall.ENXIO
			}
			if err := fifoSleep(ctx, fifoOpenRetry); err != nil {
				return windows.InvalidHandle, err
			}
		case windows.ERROR_PIPE_BUSY:
			// Readers exist but every instance is taken; the kernel can
			// wait for one better than a sleep can.
			waitNamedPipe(name16, fifoBusyWaitMs)
		default:
			return windows.InvalidHandle, err
		}
	}
}

// fifoOpenReadWrite is O_RDWR — bash's `exec 9<> a.pipe`. Per the document
// it is a reader instance we own plus one client of our own connected to
// the same name, so that bytes written come back on read: the Linux
// O_RDWR-FIFO behaviour read2.sub relies on. It never blocks.
//
// The document allows the self-connecting client to land on some other
// reader's instance if one happens to be listening; what it must not do is
// wedge when that happens. So the client is hung up and redialled until our
// own instance is the one that took it, which it will be as soon as the
// other reader is busy.
//
// The two handles are then spliced onto one (see fifoLoopback), because a
// `<>` redirection is bound to a numbered fd as an *os.File and nothing
// else: interp's fd table is map[int]*os.File, and an io.ReadWriteCloser
// reaching it is copied through an os.Pipe — which would silently drop the
// write half and leave `echo >&9` writing into a pipe nobody reads.
func fifoOpenReadWrite(ctx context.Context, path, native string) (io.ReadWriteCloser, error) {
	h, err := createFifoInstance(native)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	done := fifoConnectAsync(h)
	fail := func(err error) (io.ReadWriteCloser, error) {
		go fifoAbandonInstance(h, native, done)
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	// connected is what happens once our own instance has taken a client:
	// w is this fd's write end, and the two handles become one file.
	connected := func(err error, w windows.Handle) (io.ReadWriteCloser, error) {
		if err == nil {
			var f *os.File
			if f, err = fifoLoopback(path, h, w); err == nil {
				return f, nil
			}
		}
		windows.CloseHandle(w)
		windows.CloseHandle(h)
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	for try := 0; try < fifoSelfConnectTries; try++ {
		w, err := fifoDial(ctx, native, windows.GENERIC_WRITE, false)
		if err != nil {
			return fail(err)
		}
		select {
		case err := <-done:
			return connected(err, w)
		case <-ctx.Done():
			windows.CloseHandle(w)
			return fail(ctx.Err())
		case <-time.After(fifoSelfConnectGrace):
			// Our client went to a different instance of the name — unless
			// it arrived just as the grace ran out, which this last look
			// settles before the client is thrown away.
			select {
			case err := <-done:
				return connected(err, w)
			default:
			}
			// Hang it up unwritten — closing, not disconnecting, so that
			// reader reads EOF rather than "no process is on the other end
			// of the pipe" — and dial again.
			windows.CloseHandle(w)
		}
	}
	return fail(syscall.ENXIO)
}

const (
	// fifoSelfConnectTries and fifoSelfConnectGrace bound the read-write
	// self-connect. One try is the answer whenever this shell's instance is
	// the only one listening, which is every case the suite exercises.
	fifoSelfConnectTries = 20
	fifoSelfConnectGrace = 100 * time.Millisecond
)

// fifoConnectAsync starts the blocking ConnectNamedPipe on its own
// goroutine so the caller can also watch ctx. The channel is buffered, so
// an abandoned connect never leaks the goroutine.
func fifoConnectAsync(h windows.Handle) <-chan error {
	done := make(chan error, 1)
	go func() { done <- connectInstance(h) }()
	return done
}

// fifoAbandonInstance releases an instance whose open was called off. The
// ConnectNamedPipe it is sitting in was issued on another goroutine's
// thread, so it cannot be cancelled; connecting to the name as a client
// wakes it instead, the same release procsubst's cleanup performs. Once it
// returns, the handle is closed and the instance is gone.
func fifoAbandonInstance(h windows.Handle, native string, done <-chan error) {
	for try := 0; try < fifoAbandonTries; try++ {
		select {
		case <-done:
			windows.CloseHandle(h)
			return
		case <-time.After(fifoOpenRetry):
		}
		// The nudge may be taken by another listener of the same name; that
		// reader reads EOF, and the loop tries again for ours.
		if name16, err := windows.UTF16PtrFromString(native); err == nil {
			if c, err := windows.CreateFile(name16, windows.GENERIC_WRITE, 0,
				nil, windows.OPEN_EXISTING, 0, 0); err == nil {
				windows.CloseHandle(c)
			}
		}
	}
	// Nothing more can be done from here; the goroutine and its handle go
	// when the process does. Giving up beats blocking the shell forever.
}

// fifoAbandonTries bounds the nudging at a few seconds.
const fifoAbandonTries = 300

// fifoSleep waits d, or returns ctx's error if that comes first.
func fifoSleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// waitNamedPipe is WaitNamedPipeW, which golang.org/x/sys/windows does not
// bind. Its failure is not interesting: the caller retries either way.
func waitNamedPipe(name *uint16, timeoutMs uint32) {
	syscall.SyscallN(procWaitNamedPipeW.Addr(),
		uintptr(unsafe.Pointer(name)), uintptr(timeoutMs))
}

// fifoLoopbackPrefix names the private duplex pipe behind one read-write
// FIFO fd. It must not be mistaken for a FIFO leaf (fifoLeafPrefix) or for
// a process substitution's pipe (fifoNamePrefix); it is neither, and no
// path on disk ever names it.
const fifoLoopbackPrefix = "sh-fifo-rw-"

// fifoLoopback presents a FIFO's server instance r and this shell's own
// client w of it as a single *os.File, which is what a `<>` redirection
// must be bound to.
//
// One handle cannot do it directly: a named pipe instance is a conduit
// between two ends, so a write to the server end arrives at the *client*,
// never back at the server, and the document fixes every FIFO instance as
// PIPE_ACCESS_INBOUND — a duplex instance would not be interoperable with
// the plain readers other opens of the same FIFO create. So a private
// duplex pipe is made instead, its client handed to the shell as the fd,
// and its server spliced onto the FIFO's two handles:
//
//	fd write -> private server -> w -> [the FIFO] -> r -> private server -> fd read
//
// Closing the fd ends the first copy, which closes w; the FIFO's r then
// reads EOF, which ends the second copy and releases the rest. Nothing has
// to be reclaimed by hand.
//
// One recorded deviation: the bytes in flight can sit in three kernel
// buffers rather than one, so a script that writes without ever reading
// blocks later than it would on a Linux FIFO. Both block; only the number
// differs, and no script in the suite depends on it.
func fifoLoopback(path string, r, w windows.Handle) (*os.File, error) {
	srv, cli, err := newFifoLoopbackPipe()
	if err != nil {
		return nil, err
	}
	rf := os.NewFile(uintptr(r), path)
	wf := os.NewFile(uintptr(w), path)
	sf := os.NewFile(uintptr(srv), path)
	go func() {
		// What the shell writes to the fd goes to the FIFO; the fd being
		// closed is read here as EOF, and hangs the FIFO client up.
		io.Copy(wf, sf)
		wf.Close()
	}()
	go func() {
		// What the FIFO delivers comes back on the fd; the FIFO closing
		// hangs the fd up in turn.
		io.Copy(sf, rf)
		rf.Close()
		sf.Close()
	}()
	return os.NewFile(uintptr(cli), path), nil
}

// newFifoLoopbackPipe creates the private duplex pipe and connects this
// process to both of its ends.
//
// The name is 128 CSPRNG bits, the instance limit is one, and
// FILE_FLAG_FIRST_PIPE_INSTANCE makes creating it exclusive — but the
// client that connects is still checked to be this process, because the fd
// carries the script's data and a connection from anywhere else would be
// both a leak of it and an injection into it.
func newFifoLoopbackPipe() (srv, cli windows.Handle, err error) {
	for try := 0; try < 100; try++ {
		native := fifoPipeNativeDir + fifoLoopbackPrefix + randomFifoHex()
		name16, err := windows.UTF16PtrFromString(native)
		if err != nil {
			return windows.InvalidHandle, windows.InvalidHandle, err
		}
		s, err := windows.CreateNamedPipe(name16,
			windows.PIPE_ACCESS_DUPLEX|windows.FILE_FLAG_FIRST_PIPE_INSTANCE,
			windows.PIPE_TYPE_BYTE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS,
			1, fifoPipeBuf, fifoPipeBuf, 0, nil)
		if err != nil {
			// FILE_FLAG_FIRST_PIPE_INSTANCE reports a name collision as
			// ERROR_ACCESS_DENIED; draw another name.
			if err == windows.ERROR_ACCESS_DENIED || err == windows.ERROR_PIPE_BUSY {
				continue
			}
			return windows.InvalidHandle, windows.InvalidHandle, err
		}
		c, err := windows.CreateFile(name16,
			windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil,
			windows.OPEN_EXISTING, 0, 0)
		if err != nil {
			windows.CloseHandle(s)
			return windows.InvalidHandle, windows.InvalidHandle, err
		}
		if err := connectInstance(s); err != nil {
			windows.CloseHandle(c)
			windows.CloseHandle(s)
			return windows.InvalidHandle, windows.InvalidHandle, err
		}
		if !fifoLoopbackClientIsOurs(s) {
			windows.CloseHandle(c)
			windows.CloseHandle(s)
			return windows.InvalidHandle, windows.InvalidHandle, syscall.EACCES
		}
		return s, c, nil
	}
	return windows.InvalidHandle, windows.InvalidHandle, windows.ERROR_PIPE_BUSY
}

// fifoLoopbackClientIsOurs reports whether the process on the client end of
// the loopback instance is this one.
func fifoLoopbackClientIsOurs(srv windows.Handle) bool {
	var pid uint32
	if err := windows.GetNamedPipeClientProcessId(srv, &pid); err != nil {
		return false
	}
	return pid == windows.GetCurrentProcessId()
}

// fifoMarkerStat gives a path carrying a marker the type POSIX gives a
// FIFO, so `test -p` and `[ ! -f ]` agree with the fact that opening it
// rendezvouses on a pipe. Every other path is returned untouched.
//
// The attribute test comes off the stat that already happened, so an
// ordinary file costs nothing here; only a small file that really is marked
// FILE_ATTRIBUTE_SYSTEM is read.
func fifoMarkerStat(path string, info fs.FileInfo) fs.FileInfo {
	if info == nil || !info.Mode().IsRegular() || info.Size() > fifoMarkerMaxLen {
		return info
	}
	if d, ok := info.Sys().(*syscall.Win32FileAttributeData); ok &&
		!fifoMarkerAttrs(d.FileAttributes) {
		return info
	}
	if _, ok, _ := fifoMarkerLeaf(path); !ok {
		return info
	}
	return fifoMarkerInfo{info}
}

// createFifoMarker writes the marker for a new FIFO at path, which must not
// exist: this is the shell's own mkfifo, and it produces bytes identical to
// the coreutils applet's because both follow format v1. The
// FILE_ATTRIBUTE_SYSTEM attribute is part of the format, not decoration —
// without it the file is not a FIFO to anybody, this opener included.
func createFifoMarker(path string, perm os.FileMode) error {
	leaf := newFifoLeaf()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	_, err = f.Write(fifoMarkerBytes(leaf))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = setFifoMarkerAttr(path)
	}
	if err != nil {
		os.Remove(path)
	}
	return err
}

func setFifoMarkerAttr(path string) error {
	name16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attrs, err := windows.GetFileAttributes(name16)
	if err != nil {
		return err
	}
	return windows.SetFileAttributes(name16, attrs|windows.FILE_ATTRIBUTE_SYSTEM)
}
