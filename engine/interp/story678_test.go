// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/qiangli/bash/pathconv"
)

// Sprint 245, story 678: platform-neutral coverage for the POSIX virtual
// root on Windows — PATH splitting, executable spelling, .exe transparency,
// cd through a mount and the logical PWD it records. Each piece takes an
// explicit windows flag, mount table or stat seam so the Windows logic is
// proven on any host.

// story678Mounts is the harness layout: BASHY_ROOT=D:\w\root (usr\bin, no
// root\bin) and TEMP on C:.
func story678Mounts() *pathconv.Mounts {
	return pathconv.NewMounts(`D:\w\root`, nil, `C:\Temp`)
}

func TestSplitLookPathWindowsMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path    string
		windows bool
		want    []string
	}{
		{"/bin:/usr/bin", true, []string{"/bin", "/usr/bin"}},
		{`C:\a;D:\b`, true, []string{`C:\a`, `D:\b`}},
		{`c:/x:/bin`, true, []string{`c:/x`, `/bin`}},
		{`;C:\bin`, true, []string{``, `C:\bin`}},
		{"/bin:/usr/bin", false, []string{"/bin", "/usr/bin"}},
	}
	for _, tt := range tests {
		if got := splitLookPath(tt.path, tt.windows); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("splitLookPath(%q, %v) = %q, want %q", tt.path, tt.windows, got, tt.want)
		}
	}
}

func TestLookPathDirPosixListWindowsMode(t *testing.T) {
	t.Parallel()

	// A ':'-separated POSIX PATH is walked element by element and each
	// candidate keeps the POSIX spelling, so a hit is reported the way
	// bash reports it (/usr/bin/sh) and the mounts resolve it later.
	var tried []string
	find := func(_ string, file string, exts []string) (string, error) {
		tried = append(tried, file)
		if file == "/usr/bin/sh" {
			return file, nil
		}
		return "", os.ErrNotExist
	}
	env := expand.ListEnviron("PATH=/bin:/usr/bin:C:\\tools")
	got, err := lookPathDirMode(`C:\work`, env, "sh", find, true)
	if err != nil {
		t.Fatalf("lookPathDirMode: %v", err)
	}
	if got != "/usr/bin/sh" {
		t.Errorf("hit = %q, want /usr/bin/sh", got)
	}
	if want := []string{"/bin/sh", "/usr/bin/sh"}; !slices.Equal(tried, want) {
		t.Errorf("attempts = %q, want %q", tried, want)
	}
	if got := lookPathJoin("/bin/", "sh", true); got != "/bin/sh" {
		t.Errorf("lookPathJoin(/bin/, sh) = %q", got)
	}
	if got := lookPathJoin("/bin", "sh", false); got != filepath.Join("/bin", "sh") {
		t.Errorf("lookPathJoin unix = %q", got)
	}
}

func TestFindExecutablePosixSpellingKeepsInput(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sh.exe"), []byte("echo ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cmd.exe"), []byte("echo ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	exts := []string{".com", ".exe"}

	// A POSIX-spelled operand found through PATHEXT keeps its spelling:
	// `type -t /bin/sh` says file and `hash -p /bin/sh` records /bin/sh.
	posix := filepath.ToSlash(filepath.Join(dir, "sh"))
	if got, err := findExecutable(dir, posix, exts); err != nil || got != posix {
		t.Errorf("findExecutable(%q) = (%q, %v), want the input spelling", posix, got, err)
	}
	// An explicit extension is returned as given.
	explicit := filepath.ToSlash(filepath.Join(dir, "sh.exe"))
	if got, err := findExecutable(dir, explicit, exts); err != nil || got != explicit {
		t.Errorf("findExecutable(%q) = (%q, %v)", explicit, got, err)
	}
	// A bare name found through PATHEXT keeps its spelling too: the exec
	// handler resolves the file (execFileWithExt), type/hash show the name.
	if got, err := findExecutable(dir, "cmd", exts); err != nil || got != "cmd" {
		t.Errorf("findExecutable(cmd) = (%q, %v), want cmd", got, err)
	}
	if got, err := findExecutable(dir, "cmd.exe", exts); err != nil || got != "cmd.exe" {
		t.Errorf("findExecutable(cmd.exe) = (%q, %v), want cmd.exe", got, err)
	}
	if _, err := findExecutable(dir, filepath.ToSlash(filepath.Join(dir, "none")), exts); err == nil {
		t.Error("findExecutable(none) succeeded")
	}
}

// fakeFileInfo is the minimal fs.FileInfo a stubbed stat returns.
type fakeFileInfo struct {
	name string
	dir  bool
}

func (f fakeFileInfo) Name() string       { return f.name }
func (f fakeFileInfo) Size() int64        { return 0 }
func (f fakeFileInfo) Mode() fs.FileMode  { return 0o755 }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return f.dir }
func (f fakeFileInfo) Sys() any           { return nil }

// story678Stat is a stat over the harness root: the directories root,
// root\usr, root\usr\bin, root\etc and C:\Temp exist, and sh only as
// sh.exe.
func story678Stat(t *testing.T) (func(string) (fs.FileInfo, error), *[]string) {
	t.Helper()
	dirs := map[string]bool{
		`D:\w\root`: true, `D:\w\root\usr`: true, `D:\w\root\usr\bin`: true,
		`D:\w\root\etc`: true, `C:\Temp`: true,
	}
	files := map[string]bool{`D:\w\root\usr\bin\sh.exe`: true, `D:\w\root\etc\passwd`: true}
	var seen []string
	base := func(p string) string { return p[strings.LastIndex(p, `\`)+1:] }
	return func(p string) (fs.FileInfo, error) {
		seen = append(seen, p)
		switch {
		case dirs[p]:
			return fakeFileInfo{name: base(p), dir: true}, nil
		case files[p]:
			return fakeFileInfo{name: base(p)}, nil
		}
		return nil, &fs.PathError{Op: "stat", Path: p, Err: fs.ErrNotExist}
	}, &seen
}

func TestExeRetryPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path string
		want string
		ok   bool
	}{
		{`D:\w\root\usr\bin\sh`, `D:\w\root\usr\bin\sh.exe`, true},
		{`/bin/sh`, `/bin/sh.exe`, true},
		{`sh`, `sh.exe`, true},
		{`D:\w\root\usr\bin\sh.exe`, ``, false},  // already has an extension
		{`D:\w\x.d\sh`, `D:\w\x.d\sh.exe`, true}, // the dot is in a parent
		{`D:\w\root\`, ``, false},                // a directory spelling
		{`C:`, ``, false},
		{``, ``, false},
	}
	for _, tt := range tests {
		got, ok := exeRetryPath(tt.path)
		if got != tt.want || ok != tt.ok {
			t.Errorf("exeRetryPath(%q) = (%q, %v), want (%q, %v)", tt.path, got, ok, tt.want, tt.ok)
		}
	}
}

func TestStatExeFallback(t *testing.T) {
	t.Parallel()

	stat, _ := story678Stat(t)
	notExist := &fs.PathError{Op: "stat", Path: "x", Err: fs.ErrNotExist}

	info, err := statExeFallback(stat, `D:\w\root\usr\bin\sh`, notExist, true)
	if err != nil || info == nil || info.IsDir() || info.Name() != "sh.exe" {
		t.Errorf("sh -> sh.exe: (%v, %v)", info, err)
	}
	// Only ENOENT triggers the retry; the original error otherwise.
	perm := &fs.PathError{Op: "stat", Path: "x", Err: fs.ErrPermission}
	if _, err := statExeFallback(stat, `D:\w\root\usr\bin\sh`, perm, true); err != perm {
		t.Errorf("permission error was replaced: %v", err)
	}
	// No .exe either: the original ENOENT is what the caller sees.
	if _, err := statExeFallback(stat, `D:\w\root\usr\bin\none`, notExist, true); !errors.Is(err, fs.ErrNotExist) || err != notExist {
		t.Errorf("missing both: %v", err)
	}
	// Unix never retries.
	if _, err := statExeFallback(stat, `D:\w\root\usr\bin\sh`, notExist, false); err != notExist {
		t.Errorf("unix retried: %v", err)
	}
}

func TestResolveCdPathWindowsMounts(t *testing.T) {
	t.Parallel()

	m := story678Mounts()
	stat, seen := story678Stat(t)
	noEval := func(p string) (string, error) { return "", errors.New("no symlinks") }

	// cd /bin/sh: sh.exe is a file, so bash's "Not a directory" applies.
	_, ok, err := resolveCdPathWindows(m, `C:\work`, "/bin/sh", false, stat, noEval)
	if !ok || err == nil || err.Error() != "Not a directory" {
		t.Errorf("cd /bin/sh = (ok %v, err %v), want Not a directory", ok, err)
	}
	if want := []string{`D:\w\root\usr\bin\sh`, `D:\w\root\usr\bin\sh.exe`}; !slices.Equal(*seen, want) {
		t.Errorf("stat calls = %q, want %q", *seen, want)
	}

	tests := []struct {
		operand string
		want    string
	}{
		{"/", `D:\w\root`},
		{"/usr", `D:\w\root\usr`},
		{"/bin", `D:\w\root\usr\bin`},
		{"/tmp", `C:\Temp`},
		{"/c/Temp", `C:\Temp`},
		{`C:\Temp`, `C:\Temp`},
	}
	for _, tt := range tests {
		got, ok, err := resolveCdPathWindows(m, `C:\work`, tt.operand, false, stat, noEval)
		if !ok || err != nil || got != tt.want {
			t.Errorf("cd %s = (%q, %v, %v), want %q", tt.operand, got, ok, err, tt.want)
		}
	}
	// A missing directory reports ENOENT, and a file is Not a directory.
	if _, ok, err := resolveCdPathWindows(m, `C:\work`, "/nowhere", false, stat, noEval); !ok || !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("cd /nowhere = (%v, %v)", ok, err)
	}
	if _, ok, err := resolveCdPathWindows(m, `C:\work`, "/etc/passwd", false, stat, noEval); !ok || err == nil || err.Error() != "Not a directory" {
		t.Errorf("cd /etc/passwd = (%v, %v)", ok, err)
	}
	// A relative operand is left to the component walk.
	if _, ok, _ := resolveCdPathWindows(m, `C:\work`, "sub", false, stat, noEval); ok {
		t.Error("relative operand was resolved as absolute")
	}
	// A physical cd resolves through the eval seam.
	eval := func(p string) (string, error) { return `D:\real\root`, nil }
	if got, _, err := resolveCdPathWindows(m, `C:\work`, "/", true, stat, eval); err != nil || got != `D:\real\root` {
		t.Errorf("cd -P / = (%q, %v)", got, err)
	}
}

func TestCdLogicalPWDWindowsMode(t *testing.T) {
	t.Parallel()

	m := story678Mounts()
	tests := []struct {
		name     string
		cur      string
		operand  string
		apath    string
		physical bool
		want     string
	}{
		{"posix operand keeps its spelling", "/c/work", "/tmp", `C:\Temp`, false, "/tmp"},
		{"alias keeps its spelling", "/c/work", "/bin", `D:\w\root\usr\bin`, false, "/bin"},
		{"dots are normalized as strings", "/c/work", "/usr/../usr/./bin/", `D:\w\root\usr\bin`, false, "/usr/bin"},
		{"never above root", "/c/work", "/../..", `D:\w\root`, false, "/"},
		{"relative appends to the logical dir", "/tmp", "bash-dir-a", `C:\Temp\bash-dir-a`, false, "/tmp/bash-dir-a"},
		{"dotdot from the logical dir", "/usr/bin", "..", `D:\w\root\usr`, false, "/usr"},
		{"native operand maps back", "/c/work", `C:\Temp\x`, `C:\Temp\x`, false, "/tmp/x"},
		{"drive operand maps back", "/c/work", `D:`, `D:\w\root`, false, "/"},
		{"physical maps back", "/c/work", "/bin", `D:\w\root\usr\bin`, true, "/usr/bin"},
		{"physical root", "/c/work", "/", `D:\w\root`, true, "/"},
		{"unmounted native", "/c/work", `E:\data\`, `E:\data\`, true, "/e/data"},
	}
	for _, tt := range tests {
		if got := cdLogicalPWDMode(m, tt.cur, tt.operand, tt.apath, tt.physical, true); got != tt.want {
			t.Errorf("%s: cdLogicalPWDMode(%q, %q, %q, %v) = %q, want %q",
				tt.name, tt.cur, tt.operand, tt.apath, tt.physical, got, tt.want)
		}
	}
	// Unix: the resolved path, always.
	if got := cdLogicalPWDMode(nil, "/home", "../tmp", "/tmp", false, false); got != "/tmp" {
		t.Errorf("unix = %q, want /tmp", got)
	}
}

func TestLogicalDirWindowsMode(t *testing.T) {
	t.Parallel()

	m := story678Mounts()
	tests := []struct {
		pwd  string
		dir  string
		want string
	}{
		{"/bin", `D:\w\root\usr\bin`, "/bin"},           // PWD still names r.Dir
		{"/usr/bin", `d:/W/root/usr/bin/`, "/usr/bin"},  // NTFS equality
		{"/tmp", `C:\Temp`, "/tmp"},                     // through the /tmp mount
		{"/garbage", `D:\w\root\usr\bin`, "/usr/bin"},   // reassigned PWD is ignored
		{"/usr/./bin", `D:\w\root\usr\bin`, "/usr/bin"}, // not a logical name
		{`C:\Temp`, `C:\Temp`, "/tmp"},                  // PWD must be POSIX-spelled
		{"", `C:\Users\me`, "/c/Users/me"},              // no PWD: drive rule
		{"/c/Users/me", `C:\Users\me`, "/c/Users/me"},
	}
	for _, tt := range tests {
		if got := logicalDirMode(m, tt.pwd, tt.dir, true); got != tt.want {
			t.Errorf("logicalDirMode(%q, %q) = %q, want %q", tt.pwd, tt.dir, got, tt.want)
		}
	}
	if got := logicalDirMode(nil, "/elsewhere", "/home/x", false); got != "/home/x" {
		t.Errorf("unix = %q, want r.Dir", got)
	}
}

func TestCanonPosixPath(t *testing.T) {
	t.Parallel()

	tests := []struct{ in, want string }{
		{"/", "/"},
		{"//", "/"},
		{"/tmp/", "/tmp"},
		{"/a//b/./c", "/a/b/c"},
		{"/a/b/../c", "/a/c"},
		{"/a/../../b", "/b"},
		{"/..", "/"},
		{"/a/b/..", "/a"},
	}
	for _, tt := range tests {
		if got := canonPosixPath(tt.in); got != tt.want {
			t.Errorf("canonPosixPath(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestNativeExecEnvMounts(t *testing.T) {
	t.Parallel()

	m := story678Mounts()
	env := []string{
		"PATH=/bin:/usr/bin:/c/Go/bin",
		"TMPDIR=/tmp",
		"HOME=/c/Users/me",
		"OTHER=/bin",
	}
	// Story 682: only PATH is converted; every other value reaches the
	// child in the spelling the script gave it.
	want := []string{
		`PATH=D:\w\root\usr\bin;D:\w\root\usr\bin;C:\Go\bin`,
		"TMPDIR=/tmp",
		"HOME=/c/Users/me",
		"OTHER=/bin",
	}
	if got := nativeExecEnvMountsMode(m, slices.Clone(env), true); !slices.Equal(got, want) {
		t.Errorf("with mounts = %q, want %q", got, want)
	}
	// Without a table the bare /bin and /tmp stay as they are (today's
	// behaviour), and a ';'-separated PATH is never re-split on ':'.
	env = []string{"PATH=C:\\a b:c;/c/x", "TMPDIR=/tmp"}
	want = []string{`PATH=C:\a b:c;C:\x`, "TMPDIR=/tmp"}
	if got := nativeExecEnvMountsMode(nil, slices.Clone(env), true); !slices.Equal(got, want) {
		t.Errorf("without mounts = %q, want %q", got, want)
	}
	if got := nativeExecEnvMountsMode(m, env, false); !slices.Equal(got, env) {
		t.Errorf("unix = %q, want unchanged", got)
	}
}

type fakeDirEntry struct{ name string }

func (d fakeDirEntry) Name() string               { return d.name }
func (d fakeDirEntry) IsDir() bool                { return false }
func (d fakeDirEntry) Type() fs.FileMode          { return 0 }
func (d fakeDirEntry) Info() (fs.FileInfo, error) { return fakeFileInfo{name: d.name}, nil }

func TestDecodeDirEntries(t *testing.T) {
	t.Parallel()

	entries := []fs.DirEntry{
		fakeDirEntry{"a.b"},
		fakeDirEntry{"a\uf03ab"},
		fakeDirEntry{"a\uf02ab\uf03f"},
	}
	got := decodeDirEntries(slices.Clone(entries), true)
	names := make([]string, len(got))
	for i, e := range got {
		names[i] = e.Name()
	}
	if want := []string{"a.b", "a:b", "a*b?"}; !slices.Equal(names, want) {
		t.Errorf("decoded names = %q, want %q", names, want)
	}
	if got[0] != entries[0] {
		t.Error("an entry without encoded runes was wrapped")
	}
	if info, err := got[1].Info(); err != nil || info.Name() != "a\uf03ab" {
		t.Errorf("wrapped entry lost its Info: %v, %v", info, err)
	}
	got = decodeDirEntries(slices.Clone(entries), false)
	if got[1].Name() != "a\uf03ab" {
		t.Error("unix decoded a name")
	}
}
