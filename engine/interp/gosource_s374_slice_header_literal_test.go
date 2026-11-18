// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

//go:build full

package interp_test

import "testing"

// The statement shape of cmd/compile/internal/ssa's allocIDSlice, end to end:
// a short declaration builds an imported slice header from a local backing
// array and the next line views it as a slice of another element type, and
// freeIDSlice later views it back. The header is the program's own three
// words even though its type is imported.
func TestS374ImportedSliceHeaderLiteralInShortDeclaration(t *testing.T) {
	differGoSourceDependencyModule(t, "example.com/s374header", `package dep

import "unsafe"

type Slice struct {
	Data unsafe.Pointer
	Len  int
	Cap  int
}
`, `package main

import (
	"fmt"
	"unsafe"

	"example.com/s374header/dep"
)

type ID int32

func allocIDSlice(b []int32, n int) []ID {
	var base int32
	var derived ID
	scale := unsafe.Sizeof(base) / unsafe.Sizeof(derived)
	s := dep.Slice{
		Data: unsafe.Pointer(&b[0]),
		Len:  n,
		Cap:  cap(b) * int(scale),
	}
	return *(*[]ID)(unsafe.Pointer(&s))
}

func freeIDSlice(s []ID) []int32 {
	var base int32
	var derived ID
	scale := unsafe.Sizeof(base) / unsafe.Sizeof(derived)
	b := dep.Slice{
		Data: unsafe.Pointer(&s[0]),
		Len:  int((uintptr(len(s)) + scale - 1) / scale),
		Cap:  int((uintptr(cap(s)) + scale - 1) / scale),
	}
	return *(*[]int32)(unsafe.Pointer(&b))
}

func main() {
	b := make([]int32, 4, 8)
	b[1] = 5
	ids := allocIDSlice(b, 3)
	ids[0], ids[2] = 7, 9
	fmt.Println(len(ids), cap(ids), ids)
	back := freeIDSlice(ids)
	fmt.Println(len(back), cap(back), back)
}
`)
}

// The same statements when the importing file names unsafe and the header
// package by alias, as a linked multi-file package does: the header's Data
// field still arrives spelled by package path, unsafe.Pointer.
func TestS374ImportedSliceHeaderLiteralUnderImportAliases(t *testing.T) {
	differGoSourceDependencyModule(t, "example.com/s374headeralias", `package dep

import "unsafe"

type Slice struct {
	Data unsafe.Pointer
	Len  int
	Cap  int
}
`, `package main

import (
	"fmt"
	u "unsafe"

	h "example.com/s374headeralias/dep"
)

type ID int32

func allocIDSlice(b []int32, n int) []ID {
	var base int32
	var derived ID
	scale := u.Sizeof(base) / u.Sizeof(derived)
	s := h.Slice{
		Data: u.Pointer(&b[0]),
		Len:  n,
		Cap:  cap(b) * int(scale),
	}
	return *(*[]ID)(u.Pointer(&s))
}

func freeIDSlice(s []ID) []int32 {
	var base int32
	var derived ID
	scale := u.Sizeof(base) / u.Sizeof(derived)
	b := h.Slice{
		Data: u.Pointer(&s[0]),
		Len:  int((uintptr(len(s)) + scale - 1) / scale),
		Cap:  int((uintptr(cap(s)) + scale - 1) / scale),
	}
	return *(*[]int32)(u.Pointer(&b))
}

func main() {
	b := make([]int32, 4, 8)
	b[1] = 5
	ids := allocIDSlice(b, 3)
	ids[0], ids[2] = 7, 9
	fmt.Println(len(ids), cap(ids), ids)
	back := freeIDSlice(ids)
	fmt.Println(len(back), cap(back), back)
}
`)
}
