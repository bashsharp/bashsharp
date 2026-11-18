//go:build full

package interp_test

import "testing"

func TestGoSourceS374Bug441BlankArgumentEvaluatedOnce(t *testing.T) {
	_, stderr, err := runGoSource(t, "s374-bug441", `package main
var did int
func main() { foo(side()); foo2(side(), side()); foo3(side(), side()); T.m1(T(side())); T(1).m2(side()); if did != 7 { println("BUG: missing", 7-did, "calls") } }
func foo(_ int) {}
func foo2(_, _ int) {}
func foo3(int, int) {}
type T int
func (_ T) m1() {}
func (t T) m2(_ int) {}
func side() int { did++; return 1 }
`)
	if err != nil || stderr != "" {
		t.Fatalf("run error=%v stderr=%q", err, stderr)
	}
}

func TestGoSourceS374ImportedFileModeAliasIdentity(t *testing.T) {
	out, stderr, err := runGoSource(t, "s374-filemode-alias-identity", `package main

import (
	"fmt"
	"io/fs"
	"os"
)

func mode() fs.FileMode { return os.ModeSymlink | 0o644 }

func accept(os.FileMode) {}

func main() {
	m := mode()
	if m&os.ModeSymlink == 0 {
		panic("missing symlink bit")
	}
	var om os.FileMode = m
	accept(m)
	seen := map[os.FileMode]string{om: "ok"}
	fmt.Println(seen[m])
	switch m {
	case os.ModeSymlink | 0o644:
		fmt.Println("switch")
	default:
		panic("missed switch")
	}
}
`)
	if err != nil || stderr != "" || out != "ok\nswitch\n" {
		t.Fatalf("err=%v stdout=%q stderr=%q", err, out, stderr)
	}
}
