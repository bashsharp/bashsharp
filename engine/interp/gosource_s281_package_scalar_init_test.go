//go:build full

// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp_test

// Sprint: #281; Story: #809; Story-ID: fac7e14af4a8

import "testing"

// A package initializer for a local defined scalar must evaluate its typed
// conversion even when that local type's immediate underlying type belongs to
// an import. Keeping the expression's source text here later makes the value
// look like an ordinary string when it is boxed behind an interface receiver.
func TestS281PackageLocalImportedScalarInitializer(t *testing.T) {
	out, stderr, err := runGoSourcePackages(t, `package main

import "test/p"

func main() { p.Run() }
`, map[string]string{"a.go": `package p

import (
	"fmt"
	"go/token"
)

var nopos token.Pos

type atPos token.Pos

const constnopos token.Pos = token.NoPos

var noposn = atPos(nopos)
var constnoposn = atPos(constnopos)
var ordinary = "atPos(nopos)"
var pointer *token.Pos

type positioner interface {
	Pos() token.Pos
}

func (s atPos) Pos() token.Pos { return token.Pos(s) }

func position(p positioner) token.Pos { return p.Pos() }

func Run() {
	fmt.Printf("%d %d %d %q %t\n", noposn, constnoposn, position(noposn), ordinary, pointer == nil)
}
`})
	if err != nil || out != "0 0 0 \"atPos(nopos)\" true\n" {
		t.Fatalf("err=%v stdout=%q stderr=%q", err, out, stderr)
	}
}
