// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

//go:build unix

package interp

import (
	"errors"
	"fmt"
	"os"
	"testing"
)

func TestStatVirtualFdHonorsShellClosedDescriptor(t *testing.T) {
	hostFile, err := os.CreateTemp(t.TempDir(), "inherited-fd")
	if err != nil {
		t.Fatal(err)
	}
	defer hostFile.Close()

	fd := int(hostFile.Fd())
	runner := &Runner{}
	runner.closeFd(fd)
	if !runner.fdClosedTable[fd] {
		t.Fatal("explicit close did not record an unregistered live host fd")
	}
	info, ok, err := runner.statVirtualFd(fmt.Sprintf("/dev/fd/%d", fd))
	if !ok {
		t.Fatal("shell-closed descriptor fell through to the live host fd")
	}
	if info != nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("got info=%v err=%v, want nil and os.ErrNotExist", info, err)
	}
}
