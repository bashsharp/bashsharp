// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

//go:build windows

package interp

import (
	"syscall"
	"testing"
)

// The real thing on a Windows host: the encoded argument becomes the lone
// surrogate on the UTF-16 command line os/exec builds, and a command line
// carrying that surrogate decodes back to the raw byte the way a child's
// os.Args does.
func TestWindowsArgSurrogateThroughSyscall(t *testing.T) {
	u16, err := syscall.UTF16FromString(encodeWindowsArg("ab\xde"))
	if err != nil {
		t.Fatal(err)
	}
	want := []uint16{'a', 'b', 0xDCDE, 0}
	if len(u16) != len(want) {
		t.Fatalf("UTF16FromString = %x, want %x", u16, want)
	}
	for i := range want {
		if u16[i] != want[i] {
			t.Fatalf("UTF16FromString = %x, want %x", u16, want)
		}
	}
	back := decodeWindowsArg(syscall.UTF16ToString([]uint16{'a', 'b', 0xDCDE}))
	if back != "ab\xde" {
		t.Fatalf("decoded command line = %q, want %q", back, "ab\xde")
	}
	// Without the encoding the byte is lost to U+FFFD — the nquote4 symptom.
	lossy, _ := syscall.UTF16FromString("ab\xde")
	if lossy[2] != 0xFFFD {
		t.Fatalf("raw byte on the command line = %x, expected U+FFFD", lossy[2])
	}
}
