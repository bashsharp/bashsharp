// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

//go:build windows

package interp

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func nativeFileTimes(path string) (atime, mtime windows.Filetime, err error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return atime, mtime, err
	}
	h, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return atime, mtime, err
	}
	defer windows.CloseHandle(h)
	err = windows.GetFileTime(h, nil, &atime, &mtime)
	return atime, mtime, err
}

func rawFiletime(ft windows.Filetime) uint64 {
	return uint64(ft.HighDateTime)<<32 | uint64(ft.LowDateTime)
}

func TestWindowsWriteOnlyOpenPreservesAccessTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "redir")
	if err := os.WriteFile(path, []byte("before"), 0o666); err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(name, windows.FILE_WRITE_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	wantAtime := windows.NsecToFiletime(time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC).UnixNano())
	wantMtime := windows.NsecToFiletime(time.Date(2019, 1, 2, 3, 4, 5, 0, time.UTC).UnixNano())
	if err := windows.SetFileTime(h, nil, &wantAtime, &wantMtime); err != nil {
		windows.CloseHandle(h)
		t.Fatal(err)
	}
	if err := windows.CloseHandle(h); err != nil {
		t.Fatal(err)
	}

	beforeAtime, beforeMtime, err := nativeFileTimes(path)
	if err != nil {
		t.Fatal(err)
	}
	f, handled, err := openShareDelete(path, os.O_WRONLY|os.O_TRUNC, 0o666)
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("openShareDelete did not handle an ordinary write-only path")
	}
	if _, err := f.Write([]byte("after")); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	afterAtime, afterMtime, err := nativeFileTimes(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("before native FILETIME: atime=%#016x mtime=%#016x", rawFiletime(beforeAtime), rawFiletime(beforeMtime))
	t.Logf("after native FILETIME:  atime=%#016x mtime=%#016x", rawFiletime(afterAtime), rawFiletime(afterMtime))
	if beforeAtime != wantAtime {
		t.Fatalf("setup atime = %#016x, want %#016x", rawFiletime(beforeAtime), rawFiletime(wantAtime))
	}
	if afterAtime != beforeAtime {
		t.Errorf("write-only redirection changed atime: %#016x -> %#016x", rawFiletime(beforeAtime), rawFiletime(afterAtime))
	}
	if afterMtime == beforeMtime {
		t.Errorf("truncate and write left mtime unchanged at %#016x", rawFiletime(afterMtime))
	}
}
