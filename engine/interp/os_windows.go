// Copyright (c) 2017, Andrey Nering <andrey.nering@gmail.com>
// See LICENSE for licensing information

//go:build windows

package interp

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/qiangli/bash/winmode"
	"golang.org/x/sys/windows"
)

// mkfifo creates a FIFO at path. Windows has no filesystem node that is a
// pipe, so what lands on disk is the format-v1 marker file naming the
// \\.\pipe\ pipe every open of the path rendezvouses on — the same bytes
// the coreutils mkfifo applet writes, and the same ones openPath below
// recognises. See fifo_marker.go.
func mkfifo(path string, mode uint32) error {
	return createFifoMarker(path, os.FileMode(mode&0o777))
}

// canExec reports whether path is executable. On non-Unix platforms there
// is no per-user X_OK check, so fall back to the file mode bits already
// vetted by the caller.
func canExec(path string) bool {
	return true
}

// canRead reports whether path is readable. On non-Unix platforms there
// is no portable effective-user R_OK check here, so fall back to the file
// mode bits already vetted by the caller.
func canRead(path string) bool {
	return true
}

// processUmask returns a Unix-style default on non-Unix platforms,
// since Windows has no umask equivalent.
func processUmask() int {
	return 0o022
}

// setProcessUmask is a no-op on non-unix platforms (no umask equivalent).
func setProcessUmask(mask int) {}

// syncUmaskForChild is a no-op on non-unix platforms.
func syncUmaskForChild(mask int) (restore func()) {
	return func() {}
}

func (r *Runner) startExecCmdWithUmask(ctx context.Context, cmd *exec.Cmd, mask int) error {
	if !r.execProcessGroups {
		return r.startExecCmd(ctx, cmd)
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("create command job: %w", err)
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return fmt.Errorf("set command job limit: %w", err)
	}
	if err := r.startExecCmd(ctx, cmd); err != nil {
		windows.CloseHandle(job)
		return err
	}
	// os/exec does not expose the primary thread handle needed to resume a
	// CREATE_SUSPENDED child. Assignment immediately after Start leaves a
	// brief window in which a very fast child can spawn outside this job.
	proc, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err == nil {
		err = windows.AssignProcessToJobObject(job, proc)
		windows.CloseHandle(proc)
	}
	if err != nil {
		_ = cmd.Process.Kill()
		cmd.WaitDelay = time.Second
		_ = cmd.Wait()
		windows.CloseHandle(job)
		return fmt.Errorf("assign command to job: %w", err)
	}
	execJobs.Store(cmd, &execJob{handle: job})
	return nil
}

type execJob struct {
	mu     sync.Mutex
	handle windows.Handle
}

var execJobs sync.Map // *exec.Cmd -> *execJob

func (j *execJob) terminate() {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.handle != 0 {
		_ = windows.TerminateJobObject(j.handle, 1)
	}
}

func (j *execJob) close() {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.handle != 0 {
		_ = windows.CloseHandle(j.handle)
		j.handle = 0
	}
}

func refreshFileTimesNow(file *os.File, path string) error {
	now := time.Now()
	return os.Chtimes(path, now, now)
}

// access emulates [unix.Access] on Windows, which has no such call and no
// POSIX mode bits to answer it from.
//
// The mode it reads comes from [DefaultStatHandler], which substitutes the
// mode recorded in the file's ACL (see [winmode]) when chmod put one there.
// That is the whole reason `chmod a-r f; test -r f` can answer correctly
// here, and it is what keeps this builtin's answer identical to the chmod
// and test applets' — they read the same ACL.
//
// With no recorded mode the r and w bits are still the read-only attribute
// io/fs derives them from, and executability is not a mode question at all:
// Windows runs a file because of its extension, so -x falls back to the
// PATHEXT rule the exec lookup uses. A directory is searchable.
func (r *Runner) access(ctx context.Context, path string, mode uint32) error {
	info, err := r.lstat(ctx, path)
	if err != nil {
		return err
	}
	m := info.Mode()
	switch mode {
	case access_R_OK:
		if m&0o400 == 0 {
			return fmt.Errorf("file is not readable")
		}
	case access_W_OK:
		if m&0o200 == 0 {
			return fmt.Errorf("file is not writable")
		}
	case access_X_OK:
		if !winmode.Recorded(info) {
			// Nothing ever recorded a mode for this file, so there is no
			// POSIX opinion to honour: it was created by one of the many
			// Windows programs that do not have one, or extracted from an
			// archive by one. Windows will attempt to run any file, and so
			// does this shell — which is what it did before modes could be
			// recorded at all. Only a mode somebody actually set can take
			// execute permission away.
			return nil
		}
		if m&0o100 == 0 {
			return fmt.Errorf("file is not executable")
		}
	}
	return nil
}

// unTestOwnOrGrp implements the -O and -G unary tests. Windows has no uid
// or gid to compare, but it does have an owner SID and a primary group SID
// on every file, and a process token that says which of them are this
// shell's — which is the whole of what the two tests ask. [winmode] reads
// them, as it reads the ACL the recorded mode lives in.
//
// A file that does not exist, or one on a filesystem with no security
// descriptor to read, is false rather than an error: the same answer the
// Unix implementation gives when the stat fails.
func (r *Runner) unTestOwnOrGrp(ctx context.Context, op syntax.UnTestOperator, x string) bool {
	if _, err := r.stat(ctx, x); err != nil {
		return false
	}
	path := r.absPath(x)
	if op == syntax.TsUsrOwn {
		owned, _ := winmode.IsOwner(path)
		return owned
	}
	member, _ := winmode.InGroup(path)
	return member
}

// userGroups is bash's $GROUPS. Windows has no numeric gid (os.Getgid is
// -1), and bash always lists at least the primary group, so report a
// single group 0 rather than -1 — dynvar.tests assigns GROUPS[0]=-1 and
// expects the read-only variable NOT to read back as -1.
func userGroups() []string {
	if gid := os.Getgid(); gid >= 0 {
		return []string{strconv.Itoa(gid)}
	}
	return []string{"0"}
}

// openPath opens regular files through CreateFile with FILE_SHARE_DELETE
// added to Go's READ|WRITE share mode, so a script can `rm -f f` while the
// shell still holds `exec 9<> f`, as it can on Unix; os.OpenFile alone
// makes that fail with "resource busy" (read2.sub). The flag/perm mapping
// mirrors syscall.Open's (see windowsOpenSpecFor); devices, \\.\ and \\?\
// paths, long paths and exotic flags fall back to os.OpenFile.
func openPath(ctx context.Context, path string, flag int, perm os.FileMode) (io.ReadWriteCloser, error) {
	if err := recordedModeDenies(path, flag); err != nil {
		return nil, err
	}
	// A path carrying a FIFO marker is a FIFO: open the named pipe it
	// names, never the marker file. This has to come before every other
	// case, in particular before the O_TRUNC one — `echo x > fifo` must
	// write to the pipe and leave the marker's bytes alone.
	if leaf, ok, err := fifoMarkerLeaf(path); err != nil {
		return nil, err
	} else if ok {
		return openFifoMarker(ctx, path, leaf, flag)
	}
	if f, handled, err := openShareDelete(path, flag, perm); handled {
		if err != nil {
			return nil, err
		}
		return f, nil
	}
	return os.OpenFile(path, flag, perm)
}

// openShareDelete performs the share-delete open when path and flag are
// within its remit; handled is false when the caller should use
// os.OpenFile instead. Errors are *os.PathError like os.OpenFile's.
func openShareDelete(path string, flag int, perm os.FileMode) (f *os.File, handled bool, err error) {
	if !windowsShareDeleteEligible(path) {
		return nil, false, nil
	}
	spec, ok := windowsOpenSpecFor(flag, perm)
	if !ok {
		return nil, false, nil
	}
	pathErr := func(err error) (*os.File, bool, error) {
		return nil, true, &os.PathError{Op: "open", Path: path, Err: err}
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return pathErr(err)
	}
	// A nil SecurityAttributes leaves the handle non-inheritable, the
	// O_CLOEXEC os.OpenFile always adds; the handoff to a child bashy
	// duplicates explicitly (exec_fds_windows.go).
	h, err := windows.CreateFile(name, spec.access, spec.share, nil, spec.createmode, spec.attrs, 0)
	if err != nil {
		if err == windows.ERROR_ACCESS_DENIED && spec.attrs&win32FileFlagBackupSemantics == 0 {
			// Opening a directory for writing: report EISDIR as Go does.
			if fa, e1 := windows.GetFileAttributes(name); e1 == nil && fa&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
				err = syscall.EISDIR
			}
		}
		return pathErr(err)
	}
	// Windows treats a write as access too. Keep a shell write-only
	// redirection from advancing the file's last-access time, so that the
	// native timestamps retain the distinction used by test -N. SetFileTime
	// documents this all-ones value as a per-handle suppression request and
	// requires it immediately after CreateFile. Both GENERIC_WRITE and the
	// explicit append rights in windowsOpenSpecFor include
	// FILE_WRITE_ATTRIBUTES.
	if flag&(os.O_WRONLY|os.O_RDWR) == os.O_WRONLY {
		allOnes := windows.Filetime{LowDateTime: ^uint32(0), HighDateTime: ^uint32(0)}
		if terr := windows.SetFileTime(h, nil, &allOnes, nil); terr != nil {
			_ = windows.CloseHandle(h)
			return pathErr(terr)
		}
	}
	// Truncate after opening rather than via CREATE_ALWAYS, which would
	// replace a read-only file with a fresh one (go.dev/issue/38225).
	// windows.CreateFile drops the ERROR_ALREADY_EXISTS hint Go uses to
	// skip this for a file OPEN_ALWAYS just created; truncating an empty
	// file is a no-op, and the shell only pairs O_TRUNC with write access.
	if spec.truncate {
		if terr := windows.Ftruncate(h, 0); terr != nil {
			_ = windows.CloseHandle(h)
			return pathErr(terr)
		}
	}
	return os.NewFile(uintptr(h), path), true, nil
}

// recordedModeDenies reports the EACCES a chmod'ed mode calls for when the
// access this open asks of path is one that mode took away.
//
// Windows enforces only half of a recorded mode (see [winmode]) by itself.
// Clearing w also clears the read-only attribute every CreateFile checks,
// so `chmod a-w f; > f` fails without anyone's help — which is why the
// write half of redir12.sub passed while the read half did not. Clearing r
// leaves nothing for the platform to refuse on: the DACL is consulted only
// when the file has one this shell wrote, and even then a process holding
// SeBackupPrivilege reads straight through it. The shell therefore asks
// the same question its own `test -r` asks (see [Runner.access]) and gives
// the redirection the same answer, so the two predicates cannot disagree
// about what chmod recorded.
//
// Only a mode somebody recorded counts. A file nobody has chmod'ed has no
// POSIX opinion to honour, and the attribute-derived mode io/fs reports
// for it would refuse reads on every file of a read-only volume.
func recordedModeDenies(path string, flag int) error {
	mode, ok := winmode.Get(path)
	if !ok {
		return nil
	}
	// The owner class is the one asked about, as in [Runner.access]: the
	// shell is nearly always the owner of a file it has just chmod'ed, and
	// resolving the real class would cost a token lookup per redirection.
	var want fs.FileMode
	switch flag & (os.O_RDONLY | os.O_WRONLY | os.O_RDWR) {
	case os.O_WRONLY:
		want = 0o200
	case os.O_RDWR:
		want = 0o600
	default:
		want = 0o400
	}
	if mode.Perm()&want == want {
		return nil
	}
	// The same errno a denying ACL would have produced, so the diagnostic
	// and the exit status are indistinguishable from the enforced case.
	return &os.PathError{Op: "open", Path: path, Err: syscall.ERROR_ACCESS_DENIED}
}

func openPathAt(ctx context.Context, dir, path string, flag int, perm os.FileMode) (io.ReadWriteCloser, error) {
	return openPath(ctx, shellPathJoinAbs(dir, path), flag, perm)
}

// modifiedSinceAccessed reports whether the file's mtime is strictly
// greater than its atime — bash's `-N FILE` test operator.
//
// os.Stat preserves the native FILE_BASIC_INFORMATION timestamps in
// Win32FileAttributeData. Use those values directly, as Cygwin does when it
// fills st_atim and st_mtim. In particular, do not manufacture an access time
// when NTFS last-access updates are disabled: the last access time recorded by
// the filesystem is still the only truthful value available to a POSIX shell.
func modifiedSinceAccessed(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Win32FileAttributeData)
	if !ok {
		return false
	}
	atime := uint64(stat.LastAccessTime.HighDateTime)<<32 | uint64(stat.LastAccessTime.LowDateTime)
	mtime := uint64(stat.LastWriteTime.HighDateTime)<<32 | uint64(stat.LastWriteTime.LowDateTime)
	return mtime > atime
}

func prepareBackgroundJobCmd(ctx context.Context, cmd *exec.Cmd) {}

func recordBackgroundProcessGroup(bg *bgProc, pid int, nonPrimary bool) {}

// Foreground terminal process groups are a Unix job-control facility.
type foregroundJobTTY struct{}

func prepareForegroundJobCmd(ctx context.Context, r *Runner, cmd *exec.Cmd) *foregroundJobTTY {
	return nil
}

// foregroundExistingJob has no terminal to hand over, and needs none. A
// Windows console is not a controlling tty, this runner never creates a
// process group for a job, and there is no tcsetpgrp to call — so `fg` here
// is exactly "resume the job and wait for it", which is all jobs.tests:219
// observes. The zero foregroundJobTTY's giveTo and restore below are no-ops,
// so the shared `fg` path runs unchanged; what genuinely needs a terminal
// (SIGTTIN/SIGTTOU arbitration between a foreground and a background group)
// has no Windows analogue to refuse on behalf of.
func foregroundExistingJob(r *Runner, pgrp int) (*foregroundJobTTY, error) {
	return &foregroundJobTTY{}, nil
}

func (*foregroundJobTTY) giveTo(int) error { return nil }

func (*foregroundJobTTY) restore() error { return nil }

func waitExecCmd(ctx context.Context, cmd *exec.Cmd) (err error, user, sys time.Duration) {
	defer func() {
		if value, ok := execJobs.LoadAndDelete(cmd); ok {
			value.(*execJob).close()
		}
	}()
	err = cmd.Wait()
	user, sys = processStateCPUTimes(cmd.ProcessState)
	return err, user, sys
}

func execReplace(ctx context.Context, path string, args, env []string, stdin any, stdout any, stderr any, owned *ownedExecHandoff) (bool, error) {
	return false, nil
}

func relayAsyncOwnerSignal(sig killSig) error { return relayExecReplacementSignal(sigNum(sig)) }

func relayExecReplacementSignal(sig int) error { return ExitStatus(128 + sig) }

// A Windows console break exits with a 32-bit NT status. After the bridge has
// drained output and removed scratch, reproduce that exact process outcome.
func relayForwardedProgramDeath(num int) error {
	if num == 0xC000013A {
		os.Exit(num)
	}
	return relayExecReplacementSignal(num)
}

// inheritedFd materialises a descriptor a parent bashy handed us through
// BASHY_INHERITED_HANDLES (see adoptInheritedHandles): the registered
// handle becomes an *os.File bound into the fd tables with the access the
// parent recorded, so `read -u 3`, `cat <&3` and `echo >&10` work in the
// child as they do on Unix after WithInheritedFds.
func (r *Runner) inheritedFd(fd int) (*os.File, bool) {
	if fd < 3 || !r.inheritedFds[fd] {
		return nil, false
	}
	h, ok := r.inheritedHandles[fd]
	if !ok {
		return nil, false
	}
	f := os.NewFile(h.handle, "/dev/fd/"+strconv.Itoa(fd))
	if f == nil {
		return nil, false
	}
	r.bindInheritedFile(fd, f, h.mode)
	return f, true
}

// closeOnExecFd is a no-op off unix: Windows/plan9 don't expose the int-fd
// close-on-exec primitive (syscall.CloseOnExec there takes a Handle).
func closeOnExecFd(int) {}

// hdocServe materialises a here-document body as an already-readable,
// delete-pending temporary file. Unlike an anonymous pipe, this does not
// depend on CreatePipe's requested buffer size: Windows documents that size as
// a suggestion, and a synchronous WriteFile may wait for a reader. Reopening
// the file with FILE_SHARE_DELETE lets Remove hide it while its handle remains
// valid, giving the Unix unlinked-temp-file lifetime without a writer race.
func hdocServe(body []byte) (*os.File, error) {
	created, err := os.CreateTemp("", "bashy-hdoc-")
	if err != nil {
		return nil, err
	}
	path := created.Name()
	if err := created.Close(); err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	f, handled, err := openShareDelete(path, os.O_RDWR, 0o600)
	if err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	if !handled {
		_ = os.Remove(path)
		return nil, fmt.Errorf("cannot open here-document temp file with delete sharing")
	}
	if err := os.Remove(path); err != nil {
		f.Close()
		return nil, err
	}
	if len(body) > 0 {
		n, err := f.Write(body)
		if err == nil && n != len(body) {
			err = io.ErrShortWrite
		}
		if err != nil {
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

// On Windows ExecProcessGroups uses a Job Object instead of a process group.
func setOwnProcessGroup(cmd *exec.Cmd) bool { return true }

func signalExecCmd(cmd *exec.Cmd, sig os.Signal, group bool) {
	if group {
		if value, ok := execJobs.Load(cmd); ok {
			value.(*execJob).terminate()
			return
		}
	}
	if cmd.Process != nil {
		_ = cmd.Process.Signal(sig)
	}
}

// fileWritable has no portable access-mode probe here; fd 0 stays input-only.
func fileWritable(*os.File) bool { return false }
