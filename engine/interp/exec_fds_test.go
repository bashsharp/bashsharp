// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp

import (
	"io"
	"os"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// The Windows descriptor handoff is proven here on any host: the env
// spelling round-trips, the fd selection is deterministic and sparse-safe,
// and the Unix hook is byte-for-byte execExtraFiles.

func TestInheritedHandlesRoundTrip(t *testing.T) {
	entries := []inheritedHandleEntry{
		{3, inheritedHandle{handle: 0x1a4, mode: "r"}},
		{10, inheritedHandle{handle: 0x1b8, mode: "w"}},
		{63, inheritedHandle{handle: 0xfffffff0, mode: "rw"}},
	}
	spec := formatInheritedHandles(entries)
	if want := "3:r:0x1a4,10:w:0x1b8,63:rw:0xfffffff0"; spec != want {
		t.Fatalf("format = %q, want %q", spec, want)
	}
	got, err := parseInheritedHandles(spec)
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]inheritedHandle{
		3:  {handle: 0x1a4, mode: "r"},
		10: {handle: 0x1b8, mode: "w"},
		63: {handle: 0xfffffff0, mode: "rw"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parse = %#v, want %#v", got, want)
	}
	if got, err := parseInheritedHandles(""); err != nil || len(got) != 0 {
		t.Fatalf("empty spec = %#v, %v; want empty map", got, err)
	}
	if formatInheritedHandles(nil) != "" {
		t.Fatal("format(nil) must be empty")
	}
}

func TestParseInheritedHandlesRejectsMalformed(t *testing.T) {
	for _, spec := range []string{
		"3:r",             // missing handle
		"3:r:0x1a4:x",     // extra field
		"2:r:0x1a4",       // stdio fd
		"x:r:0x1a4",       // non-numeric fd
		"3:x:0x1a4",       // bad mode
		"3:r:1a4",         // no 0x prefix
		"3:r:0x",          // empty handle
		"3:r:0xzz",        // bad hex
		"3:r:0x0",         // NULL handle
		"3:r:0x1,3:w:0x2", // duplicate fd
		"3:r:0x1,",        // trailing separator
	} {
		if got, err := parseInheritedHandles(spec); err == nil {
			t.Errorf("parse(%q) = %#v, want error", spec, got)
		}
	}
}

func TestSelectHandoffFdsSparseAndDeterministic(t *testing.T) {
	rd, wr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer rd.Close()
	defer wr.Close()
	var sink strings.Builder
	fdTable := map[int]*os.File{
		1:  wr, // stdio slots never travel
		3:  rd,
		7:  wr,
		9:  rd,
		11: nil, // a nil entry is a hole
	}
	fdReadTable := map[int]bool{3: true, 9: true}
	fdWriteTable := map[int]io.Writer{7: wr, 9: rd, 12: &sink}
	want := []handoffFd{
		{fd: 3, mode: "r", file: rd},
		{fd: 7, mode: "w", file: wr},
		{fd: 9, mode: "rw", file: rd},
		{fd: 12, mode: "w", writer: &sink},
	}
	for i := 0; i < 5; i++ {
		got := selectHandoffFds(fdTable, fdReadTable, fdWriteTable)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("run %d: got %+v, want %+v", i, got, want)
		}
	}
	if got := selectHandoffFds(nil, nil, nil); len(got) != 0 {
		t.Fatalf("empty tables selected %+v", got)
	}
}

// A table entry whose file was closed underneath it — `{ exec 10>&1; } >log`
// leaves fd 10 on the block's closed stdout — is not an open descriptor:
// it must be skipped, not fail the exec with "use of closed file".
func TestSelectHandoffFdsSkipsClosedFiles(t *testing.T) {
	rd, wr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer rd.Close()
	defer wr.Close()
	closedRd, closedWr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	closedRd.Close()
	closedWr.Close()
	if fileOpen(closedWr) || !fileOpen(wr) || fileOpen(nil) {
		t.Fatal("fileOpen misreports a closed, open or nil file")
	}
	var sink strings.Builder
	fdTable := map[int]*os.File{
		3:  rd,
		10: closedWr, // closed under the table entry
		11: closedRd, // closed, with a live writer for the same fd
		12: closedRd, // closed, with a closed writer for the same fd
	}
	fdReadTable := map[int]bool{3: true, 11: true, 12: true}
	fdWriteTable := map[int]io.Writer{
		10: closedWr,
		11: wr,
		12: closedWr,
		13: closedWr, // closed writer only
		14: &sink,
	}
	want := []handoffFd{
		{fd: 3, mode: "r", file: rd},
		{fd: 11, mode: "rw", file: wr},
		{fd: 14, mode: "w", writer: &sink},
	}
	got := selectHandoffFds(fdTable, fdReadTable, fdWriteTable)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestBindInheritedFile(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, tc := range []struct {
		mode        string
		read, write bool
	}{
		{"r", true, false},
		{"w", false, true},
		{"rw", true, true},
	} {
		r := &Runner{}
		r.bindInheritedFile(5, f, tc.mode)
		if r.fdTable[5] != f {
			t.Fatalf("%s: fdTable[5] = %v, want f", tc.mode, r.fdTable[5])
		}
		if got := r.fdReadTable[5]; got != tc.read {
			t.Fatalf("%s: readable = %v, want %v", tc.mode, got, tc.read)
		}
		if got := r.fdWriteTable[5] != nil; got != tc.write {
			t.Fatalf("%s: writable = %v, want %v", tc.mode, got, tc.write)
		}
	}
}

func TestReplaceExecEnvEntries(t *testing.T) {
	env := []string{"A=1", "BASHY_INHERITED_HANDLES=3:r:0x1", "B=2"}
	got := replaceExecEnvEntries(env, []string{"BASHY_INHERITED_HANDLES=3:r:0x1"}, []string{"BASHY_INHERITED_HANDLES=3:r:0x9"})
	want := []string{"A=1", "B=2", "BASHY_INHERITED_HANDLES=3:r:0x9"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := replaceExecEnvEntries(env, nil, nil); !reflect.DeepEqual(got, env) {
		t.Fatalf("no-op replace changed env: %q", got)
	}
	if &got[0] == &env[0] {
		t.Fatal("result aliases the input")
	}
}

func TestChildFdsFinishOrder(t *testing.T) {
	var log []string
	c := &childFds{
		afterStart: func() { log = append(log, "afterStart") },
		cleanup:    func() { log = append(log, "cleanup") },
	}
	c.started()
	c.finish()
	want := []string{"afterStart", "afterStart", "cleanup"}
	if !reflect.DeepEqual(log, want) {
		t.Fatalf("calls = %q, want %q", log, want)
	}
	// The zero value is safe to drive.
	(&childFds{}).started()
	(&childFds{}).finish()
}

// prepareChildFds: off Windows it is execExtraFiles plus the
// BASHY_INHERITED_FDS entry; on Windows a child that is not this binary
// gets neither ExtraFiles nor any announcement.
func TestPrepareChildFdsPlatformShape(t *testing.T) {
	rd, wr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer rd.Close()
	defer wr.Close()
	var sink strings.Builder
	r := &Runner{
		fdTable:      map[int]*os.File{3: rd, 6: wr},
		fdReadTable:  map[int]bool{3: true},
		fdWriteTable: map[int]io.Writer{6: wr, 8: &sink},
	}
	// A path that is certainly not the running binary.
	fds, err := prepareChildFds(r, os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer fds.finish()
	if runtime.GOOS == "windows" {
		if fds.extraFiles != nil || fds.env != nil || fds.sysAttr != nil {
			t.Fatalf("windows non-self child got %+v, want nothing beyond stdio", fds)
		}
		return
	}
	extra, inherited, cleanup, err := r.execExtraFiles()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if len(fds.extraFiles) != len(extra) || len(extra) != 6 {
		t.Fatalf("extraFiles len = %d, execExtraFiles len = %d, want 6 (fds 3..8)", len(fds.extraFiles), len(extra))
	}
	// Slots backed by the runner's own files are identical; the bridged
	// fd 8 and the closed pad slots are fresh per call.
	for i, f := range extra {
		if f == rd || f == wr {
			if fds.extraFiles[i] != f {
				t.Fatalf("slot %d: %v, want %v", i, fds.extraFiles[i], f)
			}
		}
	}
	if want := []string{BashyInheritedFdsEnv + "=" + inherited}; !reflect.DeepEqual(fds.env, want) {
		t.Fatalf("env = %q, want %q", fds.env, want)
	}
	if inherited != "3,6,8" {
		t.Fatalf("inherited = %q, want 3,6,8", inherited)
	}
	if fds.sysAttr != nil || fds.afterStart != nil {
		t.Fatal("unix hook must not touch SysProcAttr")
	}
	// The ENOEXEC re-exec reuses the prepared set on Unix.
	again, err := prepareSelfReexecFds(r, fds)
	if err != nil || again != fds {
		t.Fatalf("prepareSelfReexecFds = %v, %v; want the same set", again, err)
	}
	// An empty runner announces nothing.
	empty, err := prepareChildFds(&Runner{}, os.DevNull)
	if err != nil || empty.extraFiles != nil || empty.env != nil {
		t.Fatalf("empty runner: %+v, %v", empty, err)
	}
}

// adoptInheritedHandles is inert off Windows and never registers fds there.
func TestAdoptInheritedHandlesOffWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows adopts; see exec_fds_windows_test.go")
	}
	r := &Runner{}
	r.adoptInheritedHandles("3:r:0x1a4")
	if r.inheritedFds != nil || r.inheritedHandles != nil {
		t.Fatalf("unix runner adopted %v / %v", r.inheritedFds, r.inheritedHandles)
	}
}
