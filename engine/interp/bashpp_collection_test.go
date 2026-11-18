// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp_test

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/go-quicktest/qt"
)

func TestBashPPCollectionsReadAndMutate(t *testing.T) {
	src := `func main() {
 a := [3]int{1, 2: 7}
 b := [...]string{1: "x", "y"}
 s := []int{1, 2}
 m := map[string][]int{"a": {4, 5}}
 printf '%s:%s:%s:%s:%s\n' a[0] a[2] b[1] b[2] m["a"][1]
 a[1] = 9
 s[0] = 8
 m["a"][0] = 6
 m["z"] = []int{3}
 missing := m["missing"]
 printf '%s:%s:%s:%s:%s\n' a[1] s[0] m["a"][0] m["z"][0] missing
}
main()
`
	out, stderr, err := runBashSharpCall(t, src)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "1:7:x:y:5\n9:8:6:3:null\n"))
}

func TestBashPPCollectionAssignmentValueAndReferenceSemantics(t *testing.T) {
	src := `func main() {
 a := [1][]int{{1}}
 b := a
 b[0] = []int{2}
 s := []int{3}
 t := s
 t[0] = 4
 m := map[string]int{"x": 5}
 n := m
	 n["x"] = 6
	 printf '%s:%s:%s:%s:%s:%s\n' a[0][0] b[0][0] s[0] t[0] m["x"] n["x"]
	}
	main()
	`
	out, stderr, err := runBashSharpCall(t, src)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "1:2:4:4:6:6\n"))
}

func TestBashPPCollectionSlicingNilEqualityAndRange(t *testing.T) {
	src := `type Pair struct { A int; B string }
func main() {
 a := [4]int{1, 2, 3, 4}
 s := a[1:3]
 s[0] = 20
 t := s[:1:1]
	 base := []int{7, 8, 9}
	 narrow := base[:1]
	 expanded := narrow[:3]
 var nilSlice []int
 emptySlice := []int{}
 var nilMap map[string]int
 emptyMap := map[string]int{}
 p := Pair{A: 1, B: "x"}
 q := Pair{A: 1, B: "x"}
 r := Pair{A: 2, B: "x"}
 aa := [2]int{1, 2}
 bb := [2]int{1, 2}
 cc := [2]int{2, 1}
 for i, v := range a {
  if i == 0 { a[1] = 99 }
  printf 'array:%s:%s\n' "$i" "$v"
 }
 for i, v := range s {
  if i == 0 { s[1] = 30 }
  printf 'slice:%s:%s\n' "$i" "$v"
 }
	 m := map[string]int{"b": 2, "a": 1}
	 for k, v := range m { printf 'map:%s:%s\n' "$k" "$v" }
	 nilSliceOK := nilSlice == nil
	 emptySliceOK := emptySlice == nil
	 nilMapOK := nilMap == nil
	 emptyMapOK := emptyMap == nil
	 structSame := p == q
	 structDiff := p == r
	 arraySame := aa == bb
	 arrayDiff := aa == cc
	 ptrSame := &a[0] == &a[0]
	 ptrDiff := &a[0] == &a[1]
	 ptrNonNil := &a[0] != nil
	 printf 'slice:%s:%s:%s:%s nil:%s:%s mapnil:%s:%s eq:%s:%s:%s:%s ptr:%s:%s:%s\n' a[1] t[0] s[1] expanded[2] "$nilSliceOK" "$emptySliceOK" "$nilMapOK" "$emptyMapOK" "$structSame" "$structDiff" "$arraySame" "$arrayDiff" "$ptrSame" "$ptrDiff" "$ptrNonNil"
}
main()
`
	out, stderr, err := runBashSharpCall(t, src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s stdout: %s", stderr, out))
	qt.Assert(t, qt.Equals(stderr, ""))
	want := "array:0:1\narray:1:20\narray:2:3\narray:3:4\n" +
		"slice:0:99\nslice:1:30\nmap:a:1\nmap:b:2\n" +
		"slice:99:99:30:9 nil:true:false mapnil:true:false eq:true:false:true:false ptr:true:false:true\n"
	qt.Assert(t, qt.Equals(out, want))
}

func TestBashPPCollectionComparisonDiagnostics(t *testing.T) {
	tests := []struct{ name, body, want string }{
		{"slice", "s := []int{1}\n _ := s == s", "BASHPP-ECOMPARE-NONCOMPARABLE:"},
		{"nil slices", "var a []int\n var b []int\n _ := a == b", "BASHPP-ECOMPARE-NONCOMPARABLE:"},
		{"map", "m := map[string]int{}\n _ := m != m", "BASHPP-ECOMPARE-NONCOMPARABLE:"},
		{"nil maps", "var a map[string]int\n var b map[string]int\n _ := a == b", "BASHPP-ECOMPARE-NONCOMPARABLE:"},
		{"nil with nil", "_ := nil == nil", "BASHPP-ECOMPARE-TYPE:"},
		{"pointer types", "i := 1\n s := \"x\"\n _ := &i == &s", "BASHPP-ECOMPARE-TYPE:"},
		{"nil deref", "var p *int\n _ := *p == 0", "BASHPP-ENIL-DEREF:"},
		{"slice bounds", "s := []int{1}\n _ := s[0:2]", "BASHPP-ECOLLECTION-SLICE:"},
		{"full slice bounds", "s := []int{1, 2}\n _ := s[0:1:3]", "BASHPP-ECOLLECTION-SLICE:"},
		{"clipped capacity", "s := []int{1, 2}\n t := s[:1:1]\n _ := t[:2]", "BASHPP-ECOLLECTION-SLICE:"},
		{"nil map assign", "var m map[string]int\n m[\"x\"] = 1", "BASHPP-ENIL-MAP:"},
		{"readonly slice alias", "s := []int{1}\n readonly s\n t := s[:]\n t[0] = 2", "BASHPP-EREADONLY-MUTATION:"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			src := "func main() {\n" + test.body + "\n}\nmain()\n"
			_, stderr, err := runBashSharpCall(t, src)
			qt.Assert(t, qt.ErrorIs(err, interp.ExitStatus(2)))
			if !strings.HasPrefix(stderr, test.want) {
				t.Fatalf("stderr %q does not start with %q", stderr, test.want)
			}
		})
	}
}

func TestBashPPCollectionRangeBranchControl(t *testing.T) {
	src := `func main() {
 values := []int{10, 20, 30}
 for i, v := range values {
  if i == 0 { continue }
  if i == 2 { break }
  printf '%s:%s\n' "$i" "$v"
 }
}
main()
`
	out, stderr, err := runBashSharpCall(t, src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "1:20\n"))
}

func TestBashPPCollectionDiagnostics(t *testing.T) {
	tests := []struct{ name, src, want string }{
		{"length", `x := [999999999999999999999999]int{1}`, "BASHPP-ECOLLECTION-LENGTH:"},
		{"element", `x := []int{"bad"}`, "BASHPP-ECOLLECTION-ELEMENT:"},
		{"map element", `x := map[string]int{1}`, "BASHPP-ECOLLECTION-MAP-ELEMENT:"},
		{"map key", `x := map[[]int]int{}`, "BASHPP-ECOLLECTION-KEY:"},
		{"duplicate index", `x := []int{0: 1, 0: 2}`, "BASHPP-ECOLLECTION-DUPLICATE:"},
		{"duplicate key", `x := map[string]int{"a": 1, "a": 2}`, "BASHPP-ECOLLECTION-DUPLICATE:"},
		{"literal bounds", `x := [1]int{1: 2}`, "BASHPP-ECOLLECTION-BOUNDS:"},
		{"read bounds", "func main() {\n x := []int{1}\n y := x[2]\n}\nmain()", "BASHPP-ECOLLECTION-BOUNDS:"},
		{"assign bounds", "func main() {\n x := []int{1}\n x[2] = 3\n}\nmain()", "BASHPP-ECOLLECTION-BOUNDS:"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, stderr, err := runBashSharpCall(t, test.src+"\n")
			qt.Assert(t, qt.ErrorIs(err, interp.ExitStatus(2)))
			if !strings.HasPrefix(stderr, test.want) {
				t.Fatalf("stderr %q does not start with %q", stderr, test.want)
			}
		})
	}
}
