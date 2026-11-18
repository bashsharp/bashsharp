// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

//go:build full

package interp_test

import "testing"

// The pooled-slice shape of cmd/compile/internal/ssa's allocators: a pointer
// to a slice of method-bearing values is parked in a sync.Pool held in a
// package-level array and taken back later. A pool may drop anything put in
// it, so the output depends only on what the program can rely on.
func TestS374SyncPoolOfSlicePointers(t *testing.T) {
	differGoSource(t, `package main

import (
	"fmt"
	"sync"
)

type Value struct{ ID int }

func (v *Value) String() string { return fmt.Sprintf("v%d", v.ID) }

var poolFreeValueSlice [3]sync.Pool

type Cache struct{ hdrValueSlice []*[]*Value }

func (c *Cache) allocValueSlice(n int) []*Value {
	var s []*Value
	v := poolFreeValueSlice[1].Get()
	if v == nil {
		s = make([]*Value, 8)
	} else {
		sp := v.(*[]*Value)
		s = *sp
		*sp = nil
		c.hdrValueSlice = append(c.hdrValueSlice, sp)
	}
	return s[:n]
}

func (c *Cache) freeValueSlice(s []*Value) {
	for i := range s {
		s[i] = nil
	}
	var sp *[]*Value
	if len(c.hdrValueSlice) == 0 {
		sp = new([]*Value)
	} else {
		sp = c.hdrValueSlice[len(c.hdrValueSlice)-1]
		c.hdrValueSlice[len(c.hdrValueSlice)-1] = nil
		c.hdrValueSlice = c.hdrValueSlice[:len(c.hdrValueSlice)-1]
	}
	*sp = s
	poolFreeValueSlice[1].Put(sp)
}

func main() {
	c := &Cache{}
	for round := 0; round < 3; round++ {
		s := c.allocValueSlice(3)
		for i := range s {
			if s[i] != nil {
				panic("pooled slice was not cleared")
			}
			s[i] = &Value{ID: round*10 + i}
		}
		fmt.Println(len(s), cap(s), s[0], s[2])
		c.freeValueSlice(s)
	}
}
`, nil, "")
}
