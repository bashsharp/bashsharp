// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

//go:build linux || darwin

package interp_test

import (
	"strings"
	"testing"

	"github.com/go-quicktest/qt"
)

func TestS248NativeFaultHonorsSetPanicOnFault(t *testing.T) {
	t.Parallel()

	source := `package main

import (
	"fmt"
	"runtime/debug"
	"syscall"
)

func memcopy(dst, src []byte) (n int, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = r.(error)
		}
	}()
	for i := 0; i < len(dst) && i < len(src); i++ {
		dst[i] = src[i]
		n++
	}
	return
}

func main() {
	debug.SetPanicOnFault(true)
	size := syscall.Getpagesize()
	data, err := syscall.Mmap(-1, 0, 16*size, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_ANON|syscall.MAP_PRIVATE)
	if err != nil {
		panic(err)
	}
	hole := data[len(data)/2 : 3*(len(data)/4)]
	if err := syscall.Mprotect(hole, syscall.PROT_NONE); err != nil {
		panic(err)
	}
	const offset = 5
	n, err := memcopy(data[offset:], make([]byte, len(data)))
	if err == nil {
		panic("missing fault")
	}
	if want := len(data)/2 - offset; n != want {
		panic(fmt.Sprintf("copied %d, want %d", n, want))
	}
	fmt.Println("recovered", n)
}
`
	dir := t.TempDir()
	out, errOut, err := bashPPRunGoSource(t, dir, dir+"/fault.go", source)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", errOut))
	qt.Assert(t, qt.Equals(errOut, ""))
	qt.Assert(t, qt.Matches(out, `recovered \d+\n`))
}

func TestS248NativeFaultWithoutPanicOnFaultStaysFatal(t *testing.T) {
	t.Parallel()

	source := `package main

import "syscall"

func main() {
	size := syscall.Getpagesize()
	data, err := syscall.Mmap(-1, 0, 4*size, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_ANON|syscall.MAP_PRIVATE)
	if err != nil {
		panic(err)
	}
	hole := data[size : 2*size]
	if err := syscall.Mprotect(hole, syscall.PROT_NONE); err != nil {
		panic(err)
	}
	data[size] = 1
}
`
	dir := t.TempDir()
	out, errOut, err := bashPPRunGoSource(t, dir, dir+"/fatal.go", source)
	qt.Assert(t, qt.Not(qt.IsNil(err)), qt.Commentf("stdout: %s stderr: %s", out, errOut))
	qt.Assert(t, qt.Equals(out, ""))
	qt.Assert(t, qt.IsTrue(strings.Contains(errOut, "fatal error: fault") || strings.Contains(errOut, "unexpected fault address")), qt.Commentf("stderr: %s", errOut))
}
