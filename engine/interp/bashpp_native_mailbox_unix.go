//go:build unix

package interp

import (
	"fmt"
	"os"
	"syscall"
)

func newBashPPCallbackMailbox() (*bashPPCallbackMailbox, error) {
	// TMPDIR belongs to the interpreted program. Keep bridge storage outside
	// its runtime tree even before the helper has mapped and unlinked it.
	f, err := os.CreateTemp("/tmp", "bashpp-callback-mailbox-*")
	if err != nil {
		return nil, err
	}
	path := f.Name()
	fail := func() {
		_ = f.Close()
		_ = os.Remove(path)
	}
	if err = f.Chmod(0o600); err != nil {
		fail()
		return nil, err
	}
	if err = f.Truncate(bashPPMailboxSize); err != nil {
		fail()
		return nil, err
	}
	data, err := syscall.Mmap(int(f.Fd()), 0, bashPPMailboxSize, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		fail()
		return nil, err
	}
	return &bashPPCallbackMailbox{data: data, path: path, wake: make(chan struct{}, 1), cleanup: func() error {
		err := syscall.Munmap(data)
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		if removeErr := os.Remove(path); err == nil && !os.IsNotExist(removeErr) {
			err = removeErr
		}
		return err
	}}, nil
}

// bashPPMailboxOpenWorkerSource maps the mailbox in the dependency process and
// then unlinks it. The name is only a rendezvous — one session creates exactly
// one mailbox for exactly one helper — and both ends hold the shared mapping
// open, so removing it costs nothing and keeps the caller's TMPDIR free of
// helper storage for the whole life of the session. The host's own cleanup
// tolerates the name being gone already.
func bashPPMailboxOpenWorkerSource() string {
	return fmt.Sprintf(`func openCallbackMailbox(){if callbackMailboxPath==""{return};f,err:=os.OpenFile(callbackMailboxPath,os.O_RDWR,0);if err!=nil{return};defer f.Close();callbackMailbox,err=syscall.Mmap(int(f.Fd()),0,%d,syscall.PROT_READ|syscall.PROT_WRITE,syscall.MAP_SHARED);if err!=nil{callbackMailbox=nil;return};os.Remove(callbackMailboxPath)}`, bashPPMailboxSize)
}
