// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp

import (
	"os"
	"path/filepath"
	"strings"
)

// Windows opens regular files for the shell with FILE_SHARE_DELETE so that
// `rm -f f` succeeds while an `exec 9<> f` still holds the file, as it does
// on Unix. Go's os.OpenFile shares READ|WRITE only, so the CreateFile
// arguments are derived here, mirroring syscall.Open's mapping bit for bit
// apart from the share mode. The logic is platform-neutral so it can be
// proven on any host; os_windows.go feeds it to CreateFile.

// Win32 values used by windowsOpenSpecFor. They are stable ABI constants;
// the Windows-only test asserts them against golang.org/x/sys/windows.
const (
	win32GenericRead  = 0x80000000
	win32GenericWrite = 0x40000000

	win32FileShareRead   = 0x00000001
	win32FileShareWrite  = 0x00000002
	win32FileShareDelete = 0x00000004

	win32CreateNew    = 1
	win32OpenExisting = 3
	win32OpenAlways   = 4

	win32FileAttributeReadonly = 0x00000001
	win32FileAttributeNormal   = 0x00000080

	win32FileFlagOpenReparsePoint = 0x00200000
	win32FileFlagBackupSemantics  = 0x02000000

	win32FileAppendData      = 0x00000004
	win32FileWriteEA         = 0x00000010
	win32FileWriteAttributes = 0x00000100
	win32StandardRightsWrite = 0x00020000
	win32Synchronize         = 0x00100000
)

// windowsOpenSpec is the CreateFile call windowsOpenSpecFor derived from an
// os.OpenFile flag/perm pair.
type windowsOpenSpec struct {
	access     uint32
	share      uint32
	createmode uint32
	attrs      uint32
	// truncate asks for Ftruncate after a successful open, as Go does
	// instead of CREATE_ALWAYS/TRUNCATE_EXISTING (go.dev/issue/38225).
	// With OPEN_ALWAYS it applies only when the file already existed.
	truncate bool
}

// windowsOpenFlagsMask is the set of os.OpenFile flags the share-delete
// open understands; anything beyond it (O_SYNC, the FILE_FLAG_* high bits)
// is left to os.OpenFile.
const windowsOpenFlagsMask = os.O_RDONLY | os.O_WRONLY | os.O_RDWR |
	os.O_CREATE | os.O_TRUNC | os.O_APPEND | os.O_EXCL

// windowsOpenSpecFor maps flag and perm to CreateFile arguments the way
// syscall.Open on Windows does, with FILE_SHARE_DELETE added to the share
// mode. ok is false when flag carries bits outside windowsOpenFlagsMask.
func windowsOpenSpecFor(flag int, perm os.FileMode) (spec windowsOpenSpec, ok bool) {
	if flag&^windowsOpenFlagsMask != 0 {
		return spec, false
	}
	accessFlags := flag & (os.O_RDONLY | os.O_WRONLY | os.O_RDWR)
	switch accessFlags {
	case os.O_RDONLY:
		spec.access = win32GenericRead
	case os.O_WRONLY:
		spec.access = win32GenericWrite
	case os.O_RDWR:
		spec.access = win32GenericRead | win32GenericWrite
	default:
		return spec, false
	}
	if flag&os.O_CREATE != 0 {
		spec.access |= win32GenericWrite
	}
	if flag&os.O_APPEND != 0 {
		// GENERIC_WRITE includes FILE_WRITE_DATA, which would write at the
		// current offset rather than the end; drop it unless O_TRUNC needs
		// it, and grant the remaining write rights explicitly.
		if flag&os.O_TRUNC == 0 {
			spec.access &^= win32GenericWrite
		}
		spec.access |= win32FileAppendData | win32FileWriteAttributes | win32FileWriteEA | win32StandardRightsWrite | win32Synchronize
	}
	spec.share = win32FileShareRead | win32FileShareWrite | win32FileShareDelete
	spec.attrs = win32FileAttributeNormal
	if perm&0o200 == 0 {
		spec.attrs = win32FileAttributeReadonly
	}
	switch accessFlags {
	case os.O_WRONLY, os.O_RDWR:
		// Writing to a directory must fail (ERROR_ACCESS_DENIED → EISDIR),
		// so no backup semantics here.
	default:
		spec.attrs |= win32FileFlagBackupSemantics
	}
	switch {
	case flag&(os.O_CREATE|os.O_EXCL) == os.O_CREATE|os.O_EXCL:
		spec.createmode = win32CreateNew
		spec.attrs |= win32FileFlagOpenReparsePoint
	case flag&os.O_CREATE != 0:
		spec.createmode = win32OpenAlways
	default:
		spec.createmode = win32OpenExisting
	}
	spec.truncate = flag&os.O_TRUNC != 0
	return spec, true
}

// windowsReservedDeviceNames are the DOS device names CreateFile resolves
// regardless of directory or extension (NUL, CON, COM1…); the console
// pseudo-files are included for completeness.
var windowsReservedDeviceNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true,
	"COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
	"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
	"CONIN$": true, "CONOUT$": true,
}

// windowsShareDeleteEligible reports whether path is an ordinary file path
// that the share-delete open handles. Device and namespace paths (\\.\,
// \\?\, \??\), reserved device names and paths long enough to need Go's
// \\?\ rewriting go to os.OpenFile, which knows their quirks.
func windowsShareDeleteEligible(path string) bool {
	if path == "" {
		return false
	}
	// Empirically the kernel accepts < 248 bytes without the extended
	// prefix; beyond that os.OpenFile's fixLongPath is needed.
	if len(path) >= 248 {
		return false
	}
	if len(path) >= 4 && path[:4] == `\??\` {
		return false
	}
	isSep := func(c byte) bool { return c == '\\' || c == '/' }
	if len(path) >= 4 && isSep(path[0]) && isSep(path[1]) && (path[2] == '?' || path[2] == '.') && isSep(path[3]) {
		return false
	}
	base := filepath.Base(strings.ReplaceAll(path, "\\", "/"))
	if i := strings.IndexByte(base, '.'); i > 0 {
		base = base[:i]
	}
	base = strings.TrimRight(base, " ")
	return !windowsReservedDeviceNames[strings.ToUpper(base)]
}
