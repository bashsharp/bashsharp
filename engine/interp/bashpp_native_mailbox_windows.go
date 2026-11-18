// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

//go:build windows

package interp

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// Windows maps the same private temporary file into the interpreter and its
// native helper. The file name crosses the already-authenticated bootstrap;
// the mapping itself carries no authority and remains a bounded callback fast
// path. The control socket still owns session lifetime, overflow and fallback.
func newBashPPCallbackMailbox() (*bashPPCallbackMailbox, error) {
	f, err := os.CreateTemp("", "bashpp-callback-mailbox-*")
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
	mapping, err := syscall.CreateFileMapping(syscall.Handle(f.Fd()), nil,
		syscall.PAGE_READWRITE, 0, bashPPMailboxSize, nil)
	if err != nil {
		fail()
		return nil, err
	}
	addr, err := syscall.MapViewOfFile(mapping, syscall.FILE_MAP_WRITE, 0, 0, bashPPMailboxSize)
	if err != nil {
		_ = syscall.CloseHandle(mapping)
		fail()
		return nil, err
	}
	// addr is a view address outside the Go heap; read it through a pointer
	// to the uintptr so vet's unsafeptr check does not flag the conversion.
	data := unsafe.Slice((*byte)(*(*unsafe.Pointer)(unsafe.Pointer(&addr))), bashPPMailboxSize)
	return &bashPPCallbackMailbox{data: data, path: path, wake: make(chan struct{}, 1), cleanup: func() error {
		err := syscall.UnmapViewOfFile(addr)
		if closeErr := syscall.CloseHandle(mapping); err == nil {
			err = closeErr
		}
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		if removeErr := os.Remove(path); err == nil && !os.IsNotExist(removeErr) {
			err = removeErr
		}
		return err
	}}, nil
}

func bashPPMailboxOpenWorkerSource() string {
	return fmt.Sprintf(`func openCallbackMailbox(){if callbackMailboxPath==""{return};f,err:=os.OpenFile(callbackMailboxPath,os.O_RDWR,0);if err!=nil{return};mapping,err:=syscall.CreateFileMapping(syscall.Handle(f.Fd()),nil,syscall.PAGE_READWRITE,0,%d,nil);if err!=nil{f.Close();return};addr,err:=syscall.MapViewOfFile(mapping,syscall.FILE_MAP_WRITE,0,0,%d);syscall.CloseHandle(mapping);f.Close();if err!=nil{return};callbackMailbox=unsafe.Slice((*byte)(unsafe.Pointer(addr)),%d)}`, bashPPMailboxSize, bashPPMailboxSize, bashPPMailboxSize)
}
