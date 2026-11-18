// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

//go:build full

package interp_test

import "testing"

// The shape of cmd/compile/internal/ssa's branchelim.go: a value-receiver
// method called on an element of a slice field reached through the pointer
// result of another call, b.Succs[0].Block().Succs[0].Block().
func TestS374MethodOnIndexedFieldOfCallResult(t *testing.T) {
	differGoSource(t, `package main

import "fmt"

type Block struct {
	ID    int
	Succs []Edge
}

type Edge struct {
	b *Block
	i int
}

func (e Edge) Block() *Block { return e.b }
func (e Edge) Index() int    { return e.i }

func main() {
	join := &Block{ID: 4}
	yes := &Block{ID: 2, Succs: []Edge{{join, 0}}}
	no := &Block{ID: 3, Succs: []Edge{{join, 1}}}
	b := &Block{ID: 1, Succs: []Edge{{yes, 0}, {no, 0}}}
	if b.Succs[0].Block().Succs[0].Block() != b.Succs[1].Block().Succs[0].Block() {
		panic("diamond does not rejoin")
	}
	fmt.Println(b.Succs[0].Block().Succs[0].Block().ID, b.Succs[1].Block().Succs[0].Index())
}
`, nil, "")
}
