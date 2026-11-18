//go:build full

package interp_test

// Sprint: #281; Story: #810; Story-ID: 48c1146a3ab0
//
// Regression for the ssagen TestIntrinsics failure: the very first
// registration add("runtime","slicebytetostringtmp", all...) panics with a
// duplicate amd64 key. The intrinsic map is keyed by
// intrinsicKey{arch *sys.Arch, pkg, fn} and the builder loop iterates the
// native sys.Archs slice, doing a comma-ok lookup before each store. A
// duplicate on distinct arches means distinct native pointers collide as
// struct-map-key fields. This reproduces the exact shape with imported
// pointers drawn from a native slice (unicode.GraphicRanges, []*RangeTable)
// and a named func map-value type mirroring intrinsicBuilder.

import (
	"strings"
	"testing"
)

// Baseline: distinct package-level native pointers as a struct key field must
// stay distinct map keys (worker37's preserved reproduction).
func TestS281PointerMapKeyCollision(t *testing.T) {
	out, stderr := runGoSourcePackageSet(t, "zzdiag-pointer-key", `package main
import ("fmt"; "unicode")
type key struct {
	t    *unicode.RangeTable
	name string
}
func main() {
	m := map[key]int{}
	m[key{unicode.Letter, "x"}] = 1
	m[key{unicode.Digit, "x"}] = 2
	m[key{unicode.Punct, "x"}] = 3
	fmt.Println(len(m))
}
`, nil)
	t.Logf("out=%q stderr=%q", out, stderr)
	if out != "3\n" {
		t.Fatalf("expected 3 distinct pointer-keyed entries, got out=%q stderr=%q", out, stderr)
	}
}

// The ssagen shape: iterate a native slice of pointers, build a struct key
// {pointer, string, string}, comma-ok probe then store a named func value —
// exactly intrinsicBuilders.addForArchs over sys.Archs. Every arch pointer is
// distinct so no probe may report a duplicate.
func TestS281NativeSlicePointerKeyLoop(t *testing.T) {
	out, stderr := runGoSourcePackageSet(t, "zzdiag-native-slice-pointer-key", `package main
import ("fmt"; "unicode")
type intrinsicKey struct {
	arch *unicode.RangeTable
	pkg  string
	fn   string
}
type intrinsicBuilder func() int
func main() {
	m := map[intrinsicKey]intrinsicBuilder{}
	add := func(arch *unicode.RangeTable, pkg, fn string, b intrinsicBuilder) {
		if _, found := m[intrinsicKey{arch, pkg, fn}]; found {
			panic("intrinsic already exists")
		}
		m[intrinsicKey{arch, pkg, fn}] = b
	}
	for _, rt := range unicode.GraphicRanges {
		add(rt, "runtime", "slicebytetostringtmp", func() int { return 1 })
	}
	hits := 0
 for _, rt := range unicode.GraphicRanges {
  if _, ok := m[intrinsicKey{rt, "runtime", "slicebytetostringtmp"}]; ok { hits++ }
 }
 fmt.Println(len(m), len(unicode.GraphicRanges), hits)
}
`, nil)
	t.Logf("out=%q stderr=%q", out, stderr)
	if stderr != "" {
		t.Fatalf("unexpected stderr=%q out=%q", stderr, out)
	}
	fields := strings.Fields(out)
	if len(fields) != 3 || fields[0] != fields[1] || fields[1] != fields[2] || fields[0] == "0" {
		t.Fatalf("no output; panic on duplicate native pointer key? out=%q stderr=%q", out, stderr)
	}
}

// Reflexivity: the same imported native pointer, referenced again for a
// lookup/delete, must hash to the entry it was stored under. This is what
// ssagen's intrinsics.lookup and .alias rely on after .add.
func TestS281NativePointerKeyReflexive(t *testing.T) {
	out, stderr := runGoSourcePackageSet(t, "zzdiag-native-pointer-reflexive", `package main
import ("fmt"; "unicode")
func main() {
	m := map[*unicode.RangeTable]string{}
	m[unicode.Letter] = "L"
	m[unicode.Digit] = "D"
	v, ok := m[unicode.Letter]
	w, ok2 := m[unicode.Digit]
	_, miss := m[unicode.Punct]
	fmt.Println(v, ok, w, ok2, miss, len(m))
	delete(m, unicode.Letter)
	_, gone := m[unicode.Letter]
	fmt.Println(gone, len(m))
}
`, nil)
	t.Logf("out=%q stderr=%q", out, stderr)
	if out != "L true D true false 2\nfalse 1\n" {
		t.Fatalf("native pointer key not reflexive: out=%q stderr=%q", out, stderr)
	}
}

func TestS281NativePointerNilAndDeleteOnce(t *testing.T) {
	out, stderr := runGoSourcePackageSet(t, "native-pointer-nil-delete", `package main
import("fmt";"unicode")
var calls int
func key() *unicode.RangeTable { calls++; return unicode.Letter }
func main() {
 m:=map[*unicode.RangeTable]int{unicode.Letter:7}
 delete(m,key())
 var n *unicode.RangeTable
 m[n]=3
 got:=unicode.Properties["not-a-property"]
 fmt.Println(calls,len(m),m[got])
}`, nil)
	if out != "1 1 3\n" || stderr != "" {
		t.Fatalf("out=%q stderr=%q", out, stderr)
	}
}
