// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

//go:build windows

package interp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"golang.org/x/sys/windows"
)

// Sprint 245, story 3: the Windows shape of numbered descriptors. The
// platform-neutral logic is covered in exec_fds_test.go and
// open_share_delete_test.go; these exercise the real handles.

func runWindowsFdScript(t *testing.T, dir, src string, opts ...RunnerOption) (stdout, stderr string) {
	t.Helper()
	file, err := syntax.NewParser().Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	r, err := New(append([]RunnerOption{Dir(dir), StdIO(nil, &out, &errb)}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = r.Run(ctx, file)
	r.Reset()
	return out.String(), errb.String()
}

// selfCommand is how a script runs this test binary as a child shell: with
// GOSH_PROG set (TestMain does), the child runs its first argument as a
// script through a Runner on its own stdio. The path is spelled the MSYS
// way (/c/…), as bashy hands paths to scripts.
func selfCommand(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	slashed := filepath.ToSlash(exe)
	if len(slashed) < 2 || slashed[1] != ':' {
		t.Skipf("executable %q is not a drive path", exe)
	}
	return "'/" + strings.ToLower(slashed[:1]) + slashed[2:] + "'"
}

// getHandleInformation is GetHandleInformation, which x/sys/windows does
// not wrap; flags carries HANDLE_FLAG_INHERIT for an inheritable handle.
func getHandleInformation(h uintptr) (flags uint32, err error) {
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetHandleInformation")
	r1, _, e1 := proc.Call(h, uintptr(unsafe.Pointer(&flags)))
	if r1 == 0 {
		return 0, e1
	}
	return flags, nil
}

func TestWindowsWin32ConstantsMatchSys(t *testing.T) {
	for name, pair := range map[string][2]uint32{
		"GENERIC_READ":                 {win32GenericRead, windows.GENERIC_READ},
		"GENERIC_WRITE":                {win32GenericWrite, windows.GENERIC_WRITE},
		"FILE_SHARE_READ":              {win32FileShareRead, windows.FILE_SHARE_READ},
		"FILE_SHARE_WRITE":             {win32FileShareWrite, windows.FILE_SHARE_WRITE},
		"FILE_SHARE_DELETE":            {win32FileShareDelete, windows.FILE_SHARE_DELETE},
		"CREATE_NEW":                   {win32CreateNew, windows.CREATE_NEW},
		"OPEN_EXISTING":                {win32OpenExisting, windows.OPEN_EXISTING},
		"OPEN_ALWAYS":                  {win32OpenAlways, windows.OPEN_ALWAYS},
		"FILE_ATTRIBUTE_READONLY":      {win32FileAttributeReadonly, windows.FILE_ATTRIBUTE_READONLY},
		"FILE_ATTRIBUTE_NORMAL":        {win32FileAttributeNormal, windows.FILE_ATTRIBUTE_NORMAL},
		"FILE_FLAG_OPEN_REPARSE_POINT": {win32FileFlagOpenReparsePoint, windows.FILE_FLAG_OPEN_REPARSE_POINT},
		"FILE_FLAG_BACKUP_SEMANTICS":   {win32FileFlagBackupSemantics, windows.FILE_FLAG_BACKUP_SEMANTICS},
		"FILE_APPEND_DATA":             {win32FileAppendData, windows.FILE_APPEND_DATA},
		"FILE_WRITE_EA":                {win32FileWriteEA, windows.FILE_WRITE_EA},
		"FILE_WRITE_ATTRIBUTES":        {win32FileWriteAttributes, windows.FILE_WRITE_ATTRIBUTES},
		"STANDARD_RIGHTS_WRITE":        {win32StandardRightsWrite, windows.STANDARD_RIGHTS_WRITE},
		"SYNCHRONIZE":                  {win32Synchronize, windows.SYNCHRONIZE},
	} {
		if pair[0] != pair[1] {
			t.Errorf("%s: local %#x, x/sys %#x", name, pair[0], pair[1])
		}
	}
}

// A closed stdio slot must reach the child as handle 0, the one value
// syscall.StartProcess does not DuplicateHandle.
func TestWindowsClosedExecFileIsHandleZero(t *testing.T) {
	f, err := closedExecFile()
	if err != nil {
		t.Fatal(err)
	}
	if f == nil || f.Fd() != 0 {
		t.Fatalf("closedExecFile = %v (fd %v), want handle 0", f, f.Fd())
	}
	if again, _ := closedExecFile(); again != f {
		t.Fatal("closedExecFile must return the package-held file")
	}
}

func TestWindowsPrepareChildFdsSelfHandoff(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	rd, wr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer rd.Close()
	defer wr.Close()
	var sink bytes.Buffer
	r := &Runner{
		fdTable:      map[int]*os.File{3: rd, 7: wr},
		fdReadTable:  map[int]bool{3: true},
		fdWriteTable: map[int]io.Writer{7: wr, 12: &sink},
	}
	fds, err := prepareChildFds(r, exe)
	if err != nil {
		t.Fatal(err)
	}
	if fds.extraFiles != nil {
		t.Fatalf("ExtraFiles = %v, want none on Windows", fds.extraFiles)
	}
	if len(fds.env) != 1 || !strings.HasPrefix(fds.env[0], BashyInheritedHandlesEnv+"=") {
		t.Fatalf("env = %q, want one %s entry", fds.env, BashyInheritedHandlesEnv)
	}
	for _, kv := range fds.env {
		if strings.HasPrefix(kv, BashyInheritedFdsEnv+"=") {
			t.Fatalf("Unix-only %s emitted on Windows: %q", BashyInheritedFdsEnv, kv)
		}
	}
	handles, err := parseInheritedHandles(strings.TrimPrefix(fds.env[0], BashyInheritedHandlesEnv+"="))
	if err != nil {
		t.Fatal(err)
	}
	if len(handles) != 3 || handles[3].mode != "r" || handles[7].mode != "w" || handles[12].mode != "w" {
		t.Fatalf("handles = %+v", handles)
	}
	var cmd exec.Cmd
	fds.sysAttr(&cmd)
	if got := cmd.SysProcAttr.AdditionalInheritedHandles; len(got) != 3 {
		t.Fatalf("AdditionalInheritedHandles = %v, want 3", got)
	}
	for fd, h := range handles {
		flags, err := getHandleInformation(h.handle)
		if err != nil {
			t.Fatalf("fd %d: handle %#x not open: %v", fd, h.handle, err)
		}
		if flags&windows.HANDLE_FLAG_INHERIT == 0 {
			t.Fatalf("fd %d: handle %#x is not inheritable", fd, h.handle)
		}
	}
	// The bridged writer is fed through the duplicate until the child is
	// done; the parent's copy of the handle goes away after Start.
	var written uint32
	if err := windows.WriteFile(windows.Handle(handles[12].handle), []byte("hello"), &written, nil); err != nil {
		t.Fatal(err)
	}
	fds.started()
	if _, err := getHandleInformation(handles[12].handle); err == nil {
		t.Fatal("parent duplicate still open after Start")
	}
	fds.finish()
	if sink.String() != "hello" {
		t.Fatalf("bridged writer got %q, want hello", sink.String())
	}
	// A non-self child gets nothing beyond stdio.
	other, err := prepareChildFds(r, `C:\Windows\System32\cmd.exe`)
	if err != nil || other.env != nil || other.sysAttr != nil {
		t.Fatalf("non-self child: %+v, %v", other, err)
	}
	if !isSelfExecutable(exe) || isSelfExecutable(`C:\Windows\System32\cmd.exe`) {
		t.Fatal("isSelfExecutable misidentifies the running binary")
	}
}

// The child side: a Runner adopts BASHY_INHERITED_HANDLES from its
// environment, hides the variable, and drops entries that are not open
// handles here.
func TestWindowsAdoptInheritedHandles(t *testing.T) {
	rd, wr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer rd.Close()
	dup, err := duplicateInheritableHandle(wr)
	if err != nil {
		t.Fatal(err)
	}
	wr.Close()
	spec := formatInheritedHandles([]inheritedHandleEntry{
		{7, inheritedHandle{handle: uintptr(dup), mode: "w"}},
		{9, inheritedHandle{handle: 0x3, mode: "r"}}, // never a valid handle
	})
	file, err := syntax.NewParser().Parse(strings.NewReader(
		`echo hi >&7; echo "${BASHY_INHERITED_HANDLES-unset}"; exec 7>&-`), "")
	if err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	r, err := New(Env(expand.ListEnviron(BashyInheritedHandlesEnv+"="+spec)), StdIO(nil, &out, &errb))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), file); err != nil {
		t.Fatalf("run: %v; stderr=%q", err, errb.String())
	}
	if !r.inheritedFds[7] || r.inheritedFds[9] {
		t.Fatalf("inheritedFds = %v, want only 7", r.inheritedFds)
	}
	if out.String() != "unset\n" {
		t.Fatalf("stdout = %q, want the hidden variable reported unset", out.String())
	}
	for _, kv := range r.execEnvWithFuncs() {
		if strings.HasPrefix(kv, BashyInheritedHandlesEnv+"=") {
			t.Fatalf("handoff variable leaked into a descendant environment: %q", kv)
		}
	}
	buf := make([]byte, 16)
	n, err := rd.Read(buf)
	if err != nil || string(buf[:n]) != "hi\n" {
		t.Fatalf("read through inherited fd 7: %q, %v", buf[:n], err)
	}
}

// exec 7>file; "$SELF" -c 'echo hi >&7' writes hi: the fixture-level
// contract for a bashy running bashy with descriptors open.
func TestWindowsExecHandoffToSelf(t *testing.T) {
	self := selfCommand(t)
	tmp := t.TempDir()
	out, errb := runWindowsFdScript(t, tmp, strings.Join([]string{
		`exec 7>out.txt`,
		self + ` 'echo hi >&7'`,
		`exec 7>&-`,
		`printf 'a\nb\n' >in.txt`,
		`exec 3<in.txt`,
		self + ` 'read -u 3 x; read -u 3 y; echo "got=$x$y"'`,
		`exec 3<&-`,
	}, "\n"))
	if errb != "" {
		t.Fatalf("stderr = %q", errb)
	}
	got, err := os.ReadFile(filepath.Join(tmp, "out.txt"))
	if err != nil || string(got) != "hi\n" {
		t.Fatalf("out.txt = %q, %v; want hi", got, err)
	}
	if out != "got=ab\n" {
		t.Fatalf("stdout = %q, want got=ab", out)
	}
}

// Any external command must start while a numbered fd is open: this is
// the "fork/exec ...: not supported by windows" class from the fixture log.
func TestWindowsExternalCommandWithOpenFd(t *testing.T) {
	out, errb := runWindowsFdScript(t, t.TempDir(), "exec 3</dev/null 10>ten.txt; cmd /c echo ok; echo rc=$?; exec 3<&- 10>&-")
	if !strings.Contains(out, "ok") || !strings.Contains(out, "rc=0") {
		t.Fatalf("stdout = %q, stderr = %q", out, errb)
	}
}

// `cmd >&-`: the child sees a NULL standard output, not a process handle.
func TestWindowsClosedStdoutChild(t *testing.T) {
	self := selfCommand(t)
	out, errb := runWindowsFdScript(t, t.TempDir(), self+` 'echo hi; echo rc=$? >&2' >&-`)
	if strings.Contains(out, "hi") {
		t.Fatalf("closed stdout still delivered output: %q", out)
	}
	if !strings.Contains(errb, "write error: Bad file descriptor") || !strings.Contains(errb, "rc=1") {
		t.Fatalf("stderr = %q, want a bad-descriptor write error and rc=1", errb)
	}
}

// exec 9<>f; rm f succeeds: the file is open with FILE_SHARE_DELETE.
func TestWindowsShareDeleteOpen(t *testing.T) {
	tmp := t.TempDir()
	held := filepath.Join(tmp, "a.pipe")
	file, err := syntax.NewParser().Parse(strings.NewReader("exec 9<>a.pipe"), "")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	r, err := New(Dir(tmp), StdIO(nil, &out, &out))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), file); err != nil {
		t.Fatalf("exec 9<>: %v; %q", err, out.String())
	}
	if r.fdTable[9] == nil {
		t.Fatal("fd 9 not held open")
	}
	if err := os.Remove(held); err != nil {
		t.Fatalf("rm while held: %v", err)
	}
	r.Reset()

	// The control: Go's own open still refuses.
	ctrl := filepath.Join(tmp, "ctrl.txt")
	f, err := os.OpenFile(ctrl, os.O_RDWR|os.O_CREATE, 0o666)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(ctrl); err == nil {
		f.Close()
		t.Skip("this Windows allows deleting a READ|WRITE-shared open file; nothing to prove")
	}
	f.Close()

	// The flag mapping on real files.
	ctx := context.Background()
	p := filepath.Join(tmp, "t.txt")
	if err := os.WriteFile(p, []byte("old content"), 0o666); err != nil {
		t.Fatal(err)
	}
	w, err := openPath(ctx, p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o666)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("new")); err != nil {
		t.Fatal(err)
	}
	w.Close()
	a, err := openPath(ctx, p, os.O_WRONLY|os.O_APPEND, 0o666)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Write([]byte("+more")); err != nil {
		t.Fatal(err)
	}
	a.Close()
	if got, _ := os.ReadFile(p); string(got) != "new+more" {
		t.Fatalf("after trunc+append: %q, want new+more", got)
	}
	if _, err := openPath(ctx, p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666); err == nil || !errors.Is(err, os.ErrExist) {
		t.Fatalf("O_EXCL on an existing file: %v, want ErrExist", err)
	}
	if _, err := openPath(ctx, filepath.Join(tmp, "missing"), os.O_RDONLY, 0); err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file: %v, want ErrNotExist", err)
	}
	if _, err := openPath(ctx, tmp, os.O_WRONLY, 0); err == nil || !errors.Is(err, syscall.EISDIR) {
		t.Fatalf("writing a directory: %v, want EISDIR", err)
	}
	d, err := openPath(ctx, tmp, os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("reading a directory: %v", err)
	}
	d.Close()
	n, err := openPath(ctx, "NUL", os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("NUL falls back to os.OpenFile: %v", err)
	}
	n.Close()
}
