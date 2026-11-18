// Copyright (c) 2017, Andrey Nering <andrey.nering@gmail.com>
// See LICENSE for licensing information

//go:build unix

package interp

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/qiangli/bash/interp/ownedexec"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// umaskMu serializes the read-then-restore dance for [syscall.Umask],
// which is process-wide and has no native read-only access.
var umaskMu sync.Mutex

// foregroundTTYMu covers the brief SIGTTOU-ignore window around tcsetpgrp.
// Signal dispositions are process-wide, and a shell which has just handed its
// terminal to a child is itself in a background process group when it takes
// the terminal back.
var foregroundTTYMu sync.Mutex

// processUmask returns the current process umask without permanently
// changing it. It briefly sets the umask to 0 to read the prior value,
// then restores it, all under a package-level mutex.
func processUmask() int {
	umaskMu.Lock()
	defer umaskMu.Unlock()
	m := syscall.Umask(0)
	syscall.Umask(m)
	return m
}

// setProcessUmask sets the process-wide umask. Used only when MirrorUmask is
// enabled (a standalone shell binary, one Runner per process) so external
// commands honour the shell's umask.
func setProcessUmask(mask int) {
	umaskMu.Lock()
	defer umaskMu.Unlock()
	syscall.Umask(mask)
}

// syncUmaskForChild sets the process umask to mask and returns a function that
// restores the old value. Prefer startExecCmdWithUmask for child launches so
// the set/fork/restore sequence is atomic with respect to other runners.
func syncUmaskForChild(mask int) (restore func()) {
	umaskMu.Lock()
	old := syscall.Umask(mask)
	umaskMu.Unlock()
	return func() {
		umaskMu.Lock()
		syscall.Umask(old)
		umaskMu.Unlock()
	}
}

// startExecCmdWithUmask starts cmd while the process umask is set to mask.
// The process umask is global, so hold umaskMu across cmd.Start's fork/exec
// window; otherwise a concurrent child launch can restore an older umask
// between our set and the fork.
func (r *Runner) startExecCmdWithUmask(ctx context.Context, cmd *exec.Cmd, mask int) error {
	umaskMu.Lock()
	old := syscall.Umask(mask)
	err := r.startExecCmd(ctx, cmd)
	syscall.Umask(old)
	umaskMu.Unlock()
	return err
}

func refreshFileTimesNow(file *os.File, path string) error {
	return unix.Futimes(int(file.Fd()), nil)
}

func mkfifo(path string, mode uint32) error {
	return unix.Mkfifo(path, mode)
}

// canExec reports whether the current effective user may execute path.
// Bash's PATH search uses sh_eaccess(file, X_OK) (findcmd.c), so a file
// with the execute bit set only for other users is correctly skipped.
func canExec(path string) bool {
	err := unix.Faccessat(unix.AT_FDCWD, path, unix.X_OK, unix.AT_EACCESS)
	return accessLongFile(path, unix.X_OK, err) == nil
}

// canRead reports whether the current effective user may read path.
func canRead(path string) bool {
	return unix.Faccessat(unix.AT_FDCWD, path, unix.R_OK, unix.AT_EACCESS) == nil
}

// dupPipeFd duplicates a pipe file descriptor, returning a new *os.File
// that refers to the same underlying pipe endpoint. The caller can close
// the original fd while the duplicate remains valid. This is used to
// ensure the parent process does not hold extra pipe fd references during
// pipeline execution, which would prevent EOF/SIGPIPE propagation.
func dupPipeFd(f *os.File) (*os.File, bool, error) {
	// Duplicate the fd with close-on-exec set ATOMICALLY (fcntl
	// F_DUPFD_CLOEXEC). The previous two-step syscall.Dup + CloseOnExec left
	// a window in which the new fd had no CLOEXEC: an os/exec fork running in
	// another goroutine (pipelines/subshells execute concurrently and share
	// the process fd table) that landed in that window inherited this pipe
	// endpoint. The leaked write end kept a pipe from ever reaching EOF, and
	// under sustained load intermittently corrupted an unrelated child's I/O
	// — e.g. a command's redirected output coming out empty. ForkLock
	// serialises the dup against concurrent forks as belt-and-suspenders.
	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	newFd, err := unix.FcntlInt(f.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, false, err
	}
	return os.NewFile(uintptr(newFd), f.Name()+"-dup"), true, nil
}

func (r *Runner) inheritedFd(fd int) (*os.File, bool) {
	if fd < 3 {
		return nil, false
	}
	if !r.inheritedFds[fd] {
		return nil, false
	}
	// The ambient descriptor belongs to the caller. A second os.File for its
	// raw number would close the caller's descriptor when this runner closes
	// its table or the os.File finalizer runs. Duplicate it with CLOEXEC so
	// concurrent child launches cannot inherit the temporary physical fd.
	syscall.ForkLock.RLock()
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil {
		syscall.ForkLock.RUnlock()
		return nil, false
	}
	ownedFD, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
	syscall.ForkLock.RUnlock()
	if err != nil {
		return nil, false
	}
	f := os.NewFile(uintptr(ownedFD), "/dev/fd/"+strconv.Itoa(fd))
	if f == nil {
		_ = unix.Close(ownedFD)
		return nil, false
	}
	if r.fdTable == nil {
		r.fdTable = make(map[int]*os.File)
	}
	r.fdTable[fd] = f
	switch flags & unix.O_ACCMODE {
	case unix.O_RDONLY:
		if r.fdReadTable == nil {
			r.fdReadTable = make(map[int]bool)
		}
		r.fdReadTable[fd] = true
	case unix.O_WRONLY:
		if r.fdWriteTable == nil {
			r.fdWriteTable = make(map[int]io.Writer)
		}
		r.fdWriteTable[fd] = f
	case unix.O_RDWR:
		if r.fdReadTable == nil {
			r.fdReadTable = make(map[int]bool)
		}
		if r.fdWriteTable == nil {
			r.fdWriteTable = make(map[int]io.Writer)
		}
		r.fdReadTable[fd] = true
		r.fdWriteTable[fd] = f
	}
	return f, true
}

// access is similar to checking the permission bits from [io/fs.FileInfo],
// but it also takes into account the current user's role.
func (r *Runner) access(ctx context.Context, path string, mode uint32) error {
	// TODO(v4): "access" may need to become part of a handler, like "open" or "stat".
	err := unix.Faccessat(unix.AT_FDCWD, path, mode, unix.AT_EACCESS)
	if err != nil {
		return accessLongPath(path, mode, err)
	}
	return nil
}

// unTestOwnOrGrp implements the -O and -G unary tests. If the file does not
// exist, or the current user cannot be retrieved, returns false.
func (r *Runner) unTestOwnOrGrp(ctx context.Context, op syntax.UnTestOperator, x string) bool {
	info, err := r.stat(ctx, x)
	if err != nil {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	if op == syntax.TsUsrOwn {
		euid := uint32(os.Geteuid())
		return euid == stat.Uid
	}
	fileGid := stat.Gid
	if uint32(os.Getegid()) == fileGid || uint32(os.Getgid()) == fileGid {
		return true
	}
	if gids, err := os.Getgroups(); err == nil {
		for _, g := range gids {
			if uint32(g) == fileGid {
				return true
			}
		}
	}
	return false
}

func userGroups() []string {
	seen := make(map[int]bool)
	var list []string
	add := func(g int) {
		if g >= 0 && !seen[g] {
			seen[g] = true
			list = append(list, strconv.Itoa(g))
		}
	}
	add(os.Getgid())
	add(os.Getegid())
	if gids, err := os.Getgroups(); err == nil {
		for _, g := range gids {
			add(g)
		}
	}
	return list
}

func openPath(ctx context.Context, path string, flag int, perm os.FileMode) (io.ReadWriteCloser, error) {
	info, err := os.Lstat(path)
	if err == nil && info.Mode()&os.ModeNamedPipe != 0 {
		return openFifoWithContext(ctx, path, flag, perm)
	}
	// ENXIO is not FIFO-specific. In particular, /dev/tty returns it when
	// the process has no controlling terminal; retrying that as a FIFO open
	// turns the immediate error into an infinite wait.
	return os.OpenFile(path, flag, perm)
}

func openPathAt(ctx context.Context, dir, path string, flags int, perm os.FileMode) (io.ReadWriteCloser, error) {
	if dir == "" || shellPathAbs(path) {
		return openPath(ctx, shellPathJoinAbs(dir, path), flags, perm)
	}
	dirFD := -1
	closeDirFD := false
	hc := HandlerCtx(ctx)
	if hc.runner != nil && hc.Dir == dir {
		hc.runner.ensureDirFile(dir)
	}
	if hc.runner != nil && hc.Dir == dir && hc.runner.dirFile != nil {
		dirFD = int(hc.runner.dirFile.Fd())
	} else {
		var err error
		dirFD, err = unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if err != nil {
			return nil, &os.PathError{Op: "open", Path: path, Err: err}
		}
		closeDirFD = true
	}
	if closeDirFD {
		defer unix.Close(dirFD)
	}
	open := func(flags int, perm uint32) (int, error) {
		return unix.Openat(dirFD, path, flags, perm)
	}
	var stat unix.Stat_t
	if err := unix.Fstatat(dirFD, path, &stat, unix.AT_SYMLINK_NOFOLLOW); err == nil && stat.Mode&unix.S_IFMT == unix.S_IFIFO {
		return openFifoWithContextFunc(ctx, path, flags, perm, open)
	}
	fd, err := open(flags|unix.O_CLOEXEC, uint32(perm))
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(fd), path), nil
}

func openFifoWithContext(ctx context.Context, path string, flags int, perm os.FileMode) (io.ReadWriteCloser, error) {
	return openFifoWithContextFunc(ctx, path, flags, perm, func(flags int, perm uint32) (int, error) {
		return unix.Open(path, flags, perm)
	})
}

func openFifoWithContextFunc(ctx context.Context, path string, flags int, perm os.FileMode, open func(int, uint32) (int, error)) (io.ReadWriteCloser, error) {
	if flags&unix.O_ACCMODE == unix.O_RDONLY {
		return openReadFifoWithContext(ctx, path, flags, perm, open)
	}
	nonblockFlags := flags | unix.O_NONBLOCK | unix.O_CLOEXEC
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()

	for {
		if err := ctx.Err(); err != nil {
			return nil, &os.PathError{Op: "open", Path: path, Err: err}
		}
		fd, err := open(nonblockFlags, uint32(perm))
		if err == unix.EINTR {
			// Same SIGURG interruption as the read side. Retry immediately:
			// the loop's own ctx check above is the exit.
			continue
		}
		if err == nil {
			fl, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
			if err == nil {
				_, _ = unix.FcntlInt(uintptr(fd), unix.F_SETFL, fl&^unix.O_NONBLOCK)
			}
			return os.NewFile(uintptr(fd), path), nil
		}

		if err == unix.ENXIO && flags&(os.O_WRONLY|os.O_RDWR) != 0 {
			select {
			case <-ctx.Done():
				return nil, &os.PathError{Op: "open", Path: path, Err: ctx.Err()}
			case <-ticker.C:
				continue
			}
		}

		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
}

func openReadFifoWithContext(ctx context.Context, path string, flags int, perm os.FileMode, open func(int, uint32) (int, error)) (io.ReadWriteCloser, error) {
	type result struct {
		fd  int
		err error
	}
	done := make(chan result, 1)
	go func() {
		for {
			fd, err := open(flags|unix.O_CLOEXEC, uint32(perm))
			// The Go runtime preempts goroutines with SIGURG, so a blocking
			// FIFO open is routinely interrupted long before a writer
			// arrives. The raw openat/open below is not the retrying
			// os.Open, so EINTR reaches us verbatim; reporting it would fail
			// the open precisely when it was waiting correctly. Retry unless
			// the caller has given up, in which case the cancellation branch
			// releases this goroutine with its own guard writer.
			if err == unix.EINTR && ctx.Err() == nil {
				continue
			}
			done <- result{fd, err}
			return
		}
	}()
	select {
	case res := <-done:
		if res.err != nil {
			return nil, &os.PathError{Op: "open", Path: path, Err: res.err}
		}
		return os.NewFile(uintptr(res.fd), path), nil
	case <-ctx.Done():
		// A blocking read-side FIFO open is the only portable way to preserve
		// the writer rendezvous. Connect a temporary writer to release it so
		// cancellation does not leak the opener goroutine.
		guard, _ := open(unix.O_WRONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		res := <-done
		if guard >= 0 {
			_ = unix.Close(guard)
		}
		if res.err == nil {
			_ = unix.Close(res.fd)
		}
		return nil, &os.PathError{Op: "open", Path: path, Err: ctx.Err()}
	}
}

type waitStatus = syscall.WaitStatus

func prepareBackgroundJobCmd(ctx context.Context, cmd *exec.Cmd) {
	if job, _ := ctx.Value(bashPPStreamJobKey{}).(*bashPPStreamJob); job != nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: job.group}
		return
	}
	bg, _ := ctx.Value(bgProcCtxKey{}).(*bgProc)
	if bg == nil || !bg.jobControl {
		return
	}
	attr := &syscall.SysProcAttr{Setpgid: true}
	if bg.pgrpFixed {
		attr.Pgid = int(bg.pgrp.Load())
	}
	cmd.SysProcAttr = attr
}

func recordBackgroundProcessGroup(bg *bgProc, pid int, nonPrimary bool) {
	if bg == nil || !bg.jobControl || pid <= 0 {
		return
	}
	if bg.pgrpFixed {
		return
	}
	pgrp, err := syscall.Getpgid(pid)
	if err != nil || pgrp <= 0 {
		return
	}
	// A non-primary pipeline process may seed the group identity, but the
	// primary process always wins when it publishes.
	if !nonPrimary || bg.pgrp.Load() == 0 {
		bg.pgrp.Store(int64(pgrp))
	}
}

// foregroundJobTTY is the terminal handoff for one simple foreground command.
// Pipelines deliberately opt out: their in-process representation does not
// yet have one kernel process group to hand to the terminal.
type foregroundJobTTY struct {
	fd          int
	restorePgrp int
	closeFD     bool
}

func runnerForegroundJobTTY(r *Runner) (*foregroundJobTTY, error) {
	if r == nil || !r.monitorActive() {
		return nil, fmt.Errorf("job control is not enabled")
	}
	stdin := r.origStdin
	fd := -1
	closeFD := false
	stdinIsTTY := stdin != nil && term.IsTerminal(int(stdin.Fd()))
	if stdinIsTTY {
		fd = int(stdin.Fd())
	} else {
		var err error
		fd, err = unix.Open("/dev/tty", unix.O_RDWR|unix.O_CLOEXEC, 0)
		if err != nil {
			return nil, fmt.Errorf("no controlling terminal")
		}
		closeFD = true
	}
	shellPgrp := syscall.Getpgrp()
	foreground, err := unix.IoctlGetInt(fd, unix.TIOCGPGRP)
	if err != nil {
		if closeFD {
			_ = unix.Close(fd)
		}
		return nil, fmt.Errorf("query controlling terminal foreground process group: %w", err)
	}
	if stdinIsTTY && foreground != shellPgrp {
		return nil, fmt.Errorf("shell does not own the controlling terminal")
	}
	// A noninteractive shell may enable monitor mode while its stdin and
	// output are redirected. Bash still uses /dev/tty for fg in that case.
	// Preserve the process group which owned that terminal (typically the
	// fixture harness) so the redirected shell does not strand the PTY on its
	// own process group when it exits.
	restorePgrp := shellPgrp
	if !stdinIsTTY {
		restorePgrp = foreground
	}
	return &foregroundJobTTY{fd: fd, restorePgrp: restorePgrp, closeFD: closeFD}, nil
}

func prepareForegroundJobCmd(ctx context.Context, r *Runner, cmd *exec.Cmd) *foregroundJobTTY {
	if bg, _ := ctx.Value(bgProcCtxKey{}).(*bgProc); bg != nil {
		return nil
	}
	if r == nil || !r.monitorActive() || r.pipelineOutput || ctx.Value(pipelineExecCtxKey{}) != nil {
		return nil
	}
	tty, err := runnerForegroundJobTTY(r)
	if err != nil {
		// This is either not our controlling terminal or another shell owns
		// it. Do not create a group we cannot safely foreground.
		return nil
	}
	// Transfer ownership in the forked child before exec, while Go still
	// blocks signals. A parent-side handoff after Start lets terminal reads
	// or ioctls race ahead and stop the child on SIGTTIN/SIGTTOU.
	// Foreground uses a descriptor in the parent, including our /dev/tty fd
	// when the command's stdin is redirected.
	cmd.SysProcAttr = &syscall.SysProcAttr{Foreground: true, Ctty: tty.fd}
	return tty
}

func foregroundExistingJob(r *Runner, pgrp int) (*foregroundJobTTY, error) {
	if pgrp <= 0 {
		return nil, fmt.Errorf("job has no process group")
	}
	tty, err := runnerForegroundJobTTY(r)
	if err != nil {
		return nil, err
	}
	if err := tty.giveTo(pgrp); err != nil {
		return nil, fmt.Errorf("give controlling terminal to process group %d: %w", pgrp, err)
	}
	return tty, nil
}

func (j *foregroundJobTTY) giveTo(pgrp int) error {
	err := j.setForeground(pgrp)
	if err != nil {
		j.close()
	}
	return err
}

func (j *foregroundJobTTY) restore() error {
	err := j.setForeground(j.restorePgrp)
	if closeErr := j.close(); err == nil {
		err = closeErr
	}
	return err
}

func (j *foregroundJobTTY) close() error {
	if !j.closeFD {
		return nil
	}
	j.closeFD = false
	return unix.Close(j.fd)
}

func (j *foregroundJobTTY) setForeground(pgrp int) error {
	foregroundTTYMu.Lock()
	defer foregroundTTYMu.Unlock()
	old, saved := saveSignalDisposition(syscall.SIGTTOU)
	// Bypass os/signal.Ignore: it also mutates the Go runtime's signal
	// bookkeeping, which a raw disposition restore cannot undo.
	setOSIgnore(syscall.SIGTTOU)
	// TIOCSPGRP receives a pointer to the process-group ID on both Linux
	// and Darwin (unlike the integer-valued ioctl requests).
	err := unix.IoctlSetPointerInt(j.fd, unix.TIOCSPGRP, pgrp)
	if saved {
		restoreSignalDisposition(syscall.SIGTTOU, old)
	} else {
		// A failed snapshot must not turn this short critical section into a
		// process-wide permanent ignore.
		restoreExecSignal(syscall.SIGTTOU)
	}
	return err
}

func waitExecCmd(ctx context.Context, cmd *exec.Cmd) (err error, user, sys time.Duration) {
	bg, _ := ctx.Value(bgProcCtxKey{}).(*bgProc)
	if bg == nil || !bg.jobControl {
		err = cmd.Wait()
		user, sys = processStateCPUTimes(cmd.ProcessState)
		return err, user, sys
	}
	var status syscall.WaitStatus
	for {
		var usage syscall.Rusage
		_, err := syscall.Wait4(cmd.Process.Pid, &status, syscall.WUNTRACED|syscall.WCONTINUED, &usage)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return err, 0, 0
		}
		switch {
		case status.Stopped():
			if bg.ignoreNextStop.Swap(0) != 0 {
				continue
			}
			if name, ok := signalName(status.Signal()); ok {
				bg.setStopSignal("SIG" + name)
			}
			bg.setState(jobStopped)
			continue
		case status.Continued():
			if bg.ignoreNextContinue.Swap(0) != 0 {
				continue
			}
			bg.setState(jobRunning)
			continue
		default:
			bg.setState(jobDead)
			user = time.Duration(usage.Utime.Nano())
			sys = time.Duration(usage.Stime.Nano())
			if status.Exited() && status.ExitStatus() == 0 {
				return nil, user, sys
			}
			if status.Signaled() {
				return ExitStatus(128 + status.Signal()), user, sys
			}
			if status.Exited() {
				return ExitStatus(status.ExitStatus()), user, sys
			}
			return ExitStatus(1), user, sys
		}
	}
}

func execReplace(ctx context.Context, path string, args, env []string, stdin any, stdout any, stderr any, owned *ownedExecHandoff) (bool, error) {
	hc := HandlerCtx(ctx)
	// syscall.Exec has no directory argument. The Runner keeps its working
	// directory in-process, so after a shell `cd` it can differ from the Go
	// process's cwd. A true replacement in that state would silently execute
	// from the wrong directory and, in particular, skip a chdir failure that a
	// normal child launch must report. Fall back to exec.Cmd, whose Dir applies
	// the logical cwd without mutating the embedding process.
	if !execReplaceCwdMatches(hc.Dir) {
		return false, nil
	}
	files := []struct {
		fd int
		v  any
	}{
		{0, stdin},
		{1, stdout},
		{2, stderr},
	}
	for _, entry := range files {
		if _, ok := entry.v.(*os.File); !ok {
			return false, nil
		}
	}
	// Reserve the frame descriptor before any stdio placement: the temporary
	// file could itself have been allocated fd 0-2 after a closed redirect.
	ownedFD := -1
	if owned != nil {
		base := 3
		if hc.runner != nil {
			for fd := range hc.runner.fdTable {
				if fd >= base {
					base = fd + 1
				}
			}
		}
		fd, err := unix.FcntlInt(owned.file.Fd(), unix.F_DUPFD_CLOEXEC, base)
		if err != nil {
			return true, err
		}
		ownedFD = fd
		defer unix.Close(fd)
	}
	for _, entry := range files {
		f := entry.v.(*os.File)
		if f.Fd() == ^uintptr(0) {
			// A closed standard descriptor is represented by an invalid
			// *os.File. Preserve that state across a true exec replacement.
			if err := unix.Close(entry.fd); err != nil && err != unix.EBADF {
				return true, err
			}
			continue
		}
		if int(f.Fd()) != entry.fd {
			if err := dupFD(int(f.Fd()), entry.fd); err != nil {
				return true, err
			}
		}
	}
	if hc.runner != nil {
		// Place every registered redirect fd at its target number for the
		// exec'd process, e.g. `exec sh in 9>out 7>err 4<data` must hand
		// sh descriptors 9, 7 and 4. Two hazards make the naive
		// "dup each source onto its target" loop wrong:
		//   1. fdTable iteration order is unspecified, and one entry's
		//      target may be another entry's still-live source, so a dup
		//      can clobber a descriptor not yet moved.
		//   2. a source that already sits at its target number is left
		//      untouched, but os.OpenFile set O_CLOEXEC on it, so it would
		//      be closed by the execve and never reach the command.
		// Copy all sources to private high descriptors first (above every
		// target), then dup each into place — dupFD (Dup2/Dup3 with flags
		// 0) leaves the target without close-on-exec, covering hazard 2.
		base := 3
		for fd := range hc.runner.fdTable {
			if fd >= base {
				base = fd + 1
			}
		}
		type placement struct{ tmp, target int }
		var places []placement
		for fd, f := range hc.runner.fdTable {
			if fd < 3 || f == nil {
				continue
			}
			tmp, err := unix.FcntlInt(f.Fd(), unix.F_DUPFD_CLOEXEC, base)
			if err != nil {
				return true, err
			}
			places = append(places, placement{int(tmp), fd})
		}
		for _, p := range places {
			if err := dupFD(p.tmp, p.target); err != nil {
				return true, err
			}
			_ = unix.Close(p.tmp)
		}
	}
	if ownedFD >= 0 {
		if _, err := unix.FcntlInt(uintptr(ownedFD), unix.F_SETFD, 0); err != nil {
			return true, err
		}
		env = setExecEnvValue(env, ownedexec.Marker, strconv.Itoa(ownedFD))
	}
	if len(args) == 0 {
		return true, fmt.Errorf("exec: empty argument list")
	}
	return true, syscall.Exec(path, args, env)
}

func execReplaceCwdMatches(dir string) bool {
	if dir == "" {
		return false
	}
	processDir, err := os.Stat(".")
	if err != nil {
		return false
	}
	logicalDir, err := os.Stat(dir)
	return err == nil && os.SameFile(processDir, logicalDir)
}

func relayAsyncOwnerSignal(sig killSig) error { return relayExecReplacementSignal(sig) }

// relayForwardedProgramDeath reproduces on this process the signal death of an
// interpreted Go program whose dependency process was killed by a
// parent-delivered signal the host proxied to it. num is the platform signal
// number recorded in bashPPForwardedDeath.
func relayForwardedProgramDeath(num int) error {
	return relayExecReplacementSignal(syscall.Signal(num))
}

func relayExecReplacementSignal(sig syscall.Signal) error {
	// execve resets caught dispositions to default. Do the same before
	// relaying a proxied replacement child's terminal signal to the shell
	// host, so its parent observes WIFSIGNALED rather than a normal 128+N exit.
	// Use the raw reset seam: signal.Reset cannot reliably overwrite the raw
	// dispositions installed by OSSignalResetter and can leave a Go runtime
	// handler silently swallowing the relay, leaving the shell asleep forever.
	restoreExecSignal(sig)
	if err := syscall.Kill(os.Getpid(), sig); err != nil {
		return err
	}
	// Signal delivery is asynchronous. Do not let a normal ExitStatus return
	// race ahead and turn WIFSIGNALED into an ordinary 128+N process exit.
	for {
		time.Sleep(time.Hour)
	}
}

// modifiedSinceAccessed reports whether the file's mtime is strictly
// greater than its atime — bash's `-N FILE` test operator. On
// platforms where atime is unavailable, returns false.
func modifiedSinceAccessed(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	mtim := unixStatMtime(stat)
	atim := unixStatAtime(stat)
	if mtim.Equal(atim) {
		return false
	}
	return mtim.After(atim)
}

// closeOnExecFd marks fd close-on-exec so it does not leak into spawned
// children. Unix passes the int fd straight to syscall.CloseOnExec; the
// !unix build is a no-op (Windows wants a syscall.Handle) — see os_notunix.go.
func closeOnExecFd(fd int) { syscall.CloseOnExec(fd) }

// hdocServe materialises a here-document body as a readable descriptor. It
// writes the body to a temp file, unlinks it immediately (the open fd keeps
// the data valid and the inode is reclaimed once all descriptors close), and
// rewinds it. Using a real file instead of a pipe+goroutine makes here-doc
// delivery deterministic — a goroutine-fed pipe races with the consuming
// process under load and could truncate its output to empty.
func hdocServe(body []byte) (*os.File, error) {
	f, err := os.CreateTemp("", "bashy-hdoc-")
	if err != nil {
		return nil, err
	}
	os.Remove(f.Name())
	if len(body) > 0 {
		if _, err := f.Write(body); err != nil {
			f.Close()
			return nil, err
		}
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// setOwnProcessGroup makes cmd the leader of a new process group unless job
// control already chose its group (or hands it the terminal). It reports
// whether cmd will lead a group of its own.
func setOwnProcessGroup(cmd *exec.Cmd) bool {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	attr := cmd.SysProcAttr
	if attr.Foreground {
		return false
	}
	if attr.Setpgid {
		return attr.Pgid == 0
	}
	attr.Setpgid, attr.Pgid = true, 0
	return true
}

// signalExecCmd delivers sig to a started external command: to its whole
// process group when it leads one, else to the process alone.
func signalExecCmd(cmd *exec.Cmd, sig os.Signal, group bool) {
	if cmd.Process == nil {
		return
	}
	if group {
		if num, ok := sig.(syscall.Signal); ok && cmd.Process.Pid > 0 {
			if syscall.Kill(-cmd.Process.Pid, num) == nil {
				return
			}
		}
	}
	_ = cmd.Process.Signal(sig)
}

// fileWritable reports whether f's descriptor was opened for output
// (O_WRONLY or O_RDWR). It reads the mode through SyscallConn so an inherited
// stdin is not switched to blocking mode the way f.Fd() would.
func fileWritable(f *os.File) bool {
	rc, err := f.SyscallConn()
	if err != nil {
		return false
	}
	writable := false
	_ = rc.Control(func(fd uintptr) {
		flags, err := unix.FcntlInt(fd, unix.F_GETFL, 0)
		if err != nil {
			return
		}
		mode := flags & unix.O_ACCMODE
		writable = mode == unix.O_WRONLY || mode == unix.O_RDWR
	})
	return writable
}
