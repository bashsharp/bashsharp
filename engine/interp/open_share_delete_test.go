// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp

import (
	"os"
	"strings"
	"testing"
)

// The CreateFile argument mapping mirrors syscall.Open on Windows (Go
// 1.27) with FILE_SHARE_DELETE added; each row is that mapping by hand.
func TestWindowsOpenSpecFor(t *testing.T) {
	const share = win32FileShareRead | win32FileShareWrite | win32FileShareDelete
	const appendRights = win32FileAppendData | win32FileWriteAttributes | win32FileWriteEA | win32StandardRightsWrite | win32Synchronize
	for _, tc := range []struct {
		name string
		flag int
		perm os.FileMode
		want windowsOpenSpec
	}{
		{"read", os.O_RDONLY, 0o666, windowsOpenSpec{
			access: win32GenericRead, share: share, createmode: win32OpenExisting,
			attrs: win32FileAttributeNormal | win32FileFlagBackupSemantics,
		}},
		{"readwrite", os.O_RDWR, 0o666, windowsOpenSpec{
			access: win32GenericRead | win32GenericWrite, share: share, createmode: win32OpenExisting,
			attrs: win32FileAttributeNormal,
		}},
		{"write_create_trunc", os.O_WRONLY | os.O_CREATE | os.O_TRUNC, 0o666, windowsOpenSpec{
			access: win32GenericWrite, share: share, createmode: win32OpenAlways,
			attrs: win32FileAttributeNormal, truncate: true,
		}},
		{"append_create", os.O_WRONLY | os.O_CREATE | os.O_APPEND, 0o666, windowsOpenSpec{
			access: appendRights, share: share, createmode: win32OpenAlways,
			attrs: win32FileAttributeNormal,
		}},
		{"append_trunc_keeps_write", os.O_WRONLY | os.O_APPEND | os.O_TRUNC, 0o666, windowsOpenSpec{
			access: win32GenericWrite | appendRights, share: share, createmode: win32OpenExisting,
			attrs: win32FileAttributeNormal, truncate: true,
		}},
		{"create_excl", os.O_WRONLY | os.O_CREATE | os.O_EXCL, 0o666, windowsOpenSpec{
			access: win32GenericWrite, share: share, createmode: win32CreateNew,
			attrs: win32FileAttributeNormal | win32FileFlagOpenReparsePoint,
		}},
		{"readonly_perm", os.O_WRONLY | os.O_CREATE, 0o444, windowsOpenSpec{
			access: win32GenericWrite, share: share, createmode: win32OpenAlways,
			attrs: win32FileAttributeReadonly,
		}},
		{"rdwr_create", os.O_RDWR | os.O_CREATE, 0o666, windowsOpenSpec{
			access: win32GenericRead | win32GenericWrite, share: share, createmode: win32OpenAlways,
			attrs: win32FileAttributeNormal,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := windowsOpenSpecFor(tc.flag, tc.perm)
			if !ok {
				t.Fatal("flag rejected")
			}
			if got != tc.want {
				t.Fatalf("got %+v\nwant %+v", got, tc.want)
			}
			if got.share&win32FileShareDelete == 0 {
				t.Fatal("share mode lacks FILE_SHARE_DELETE")
			}
		})
	}
	for _, flag := range []int{os.O_RDONLY | os.O_SYNC, os.O_RDWR | 0x10000000, os.O_WRONLY | os.O_RDWR} {
		if _, ok := windowsOpenSpecFor(flag, 0o666); ok {
			t.Errorf("flag %#x accepted, want fallback to os.OpenFile", flag)
		}
	}
}

func TestWindowsShareDeleteEligible(t *testing.T) {
	long := `C:\` + strings.Repeat("a", 250)
	for path, want := range map[string]bool{
		`C:\Users\me\a.pipe`:      true,
		`C:/Users/me/a.pipe`:      true,
		`relative\file.txt`:       true,
		`file`:                    true,
		`C:\tmp\null.txt`:         true, // not a device name
		`C:\tmp\console`:          true,
		``:                        false,
		`NUL`:                     false,
		`nul`:                     false,
		`C:\tmp\NUL`:              false,
		`C:\tmp\nul.txt`:          false, // device names ignore extensions
		`CON`:                     false,
		`COM1`:                    false,
		`LPT9`:                    false,
		`CONOUT$`:                 false,
		`\\.\pipe\bashy-x`:        false,
		`//./pipe/bashy-x`:        false,
		`\\?\C:\long\path`:        false,
		`\??\C:\long\path`:        false,
		`\\server\share\file.txt`: true, // UNC is an ordinary path
		long:                      false,
	} {
		if got := windowsShareDeleteEligible(path); got != want {
			t.Errorf("eligible(%q) = %v, want %v", path, got, want)
		}
	}
}
