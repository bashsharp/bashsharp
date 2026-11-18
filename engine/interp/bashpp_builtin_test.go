// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp_test

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/go-quicktest/qt"
)

func TestBashPPPredeclaredCollectionBuiltins(t *testing.T) {
	src := `type Vec[T any] []T
func main() {
	 var nilSlice []int
	 empty := []int{}
	 made := make(Vec[int], 1, 3)
 alias := made
 grown := append(made, 2, 3)
 view := alias[:3]
 spread := append(nilSlice, grown...)
 copied := make([]int, 3)
 count := copy(copied, spread)
 m := make(map[string]int, 2)
 m["x"] = 7
 ma := m
 delete(ma, "x")
	 m["y"] = 8
	 clear(ma)
	 nilLen := len(nilSlice)
	 emptyLen := len(empty)
	 grownLen := len(grown)
	 grownCap := cap(grown)
	 mapLen := len(m)
	 strLen := len("é")
	 nilSliceOK := nilSlice == nil
	 emptyNil := empty == nil
	 madeNil := made == nil
	 mapNil := m == nil
	 print("lens:", nilLen, ":", emptyLen, ":", grownLen, ":", grownCap, " ")
	 println("copy", count, copied[2], "alias", view[2], "map", mapLen, "str", strLen, "nil", nilSliceOK, emptyNil, madeNil, mapNil)
	 clear(grown)
	 low := min(4, 2, 3)
	 high := max("a", "z", "m")
	 println("clear", view[0], view[1], view[2], "order", low, high)
}
main()
`
	out, stderr, err := runBashSharpCall(t, src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "lens:0:0:3:3 copy 3 3 alias 3 map 0 str 2 nil true false false false\nclear 0 0 0 order 2 z\n"))
}

func TestBashPPPredeclaredBuiltinResidualForms(t *testing.T) {
	const src = `type Count uint64
func requireSame[T any](a T, b T) { printf 'typed:%s:%s\n' "$a" "$b" }
func main() {
 ch := make(chan int, 3)
 ch <- 7
 channelLen := len(ch)
 channelCap := cap(ch)
 close(ch)
 closedLen := len(ch)
 arrayPointer := new([4]int)
 pointerLen := len(arrayPointer)
 pointerCap := cap(arrayPointer)
 var nilArray *[5]int
 nilPointerLen := len(nilArray)
 nilPointerCap := cap(nilArray)
 bytes := make([]byte, 3)
 copied := copy(bytes, "éx")
 empty := make([]byte, 0, 3)
 appended := append(empty, "Aé"...)
 exact := max(9007199254740992, 9007199254740993)
 negativeA := -9007199254740993
 negativeB := -9007199254740992
 exactNegative := min(negativeA, negativeB)
 var count Count = 9007199254740993
 typed := min(count, 9007199254740994)
 requireSame(typed, count)
 println("exact-print", exact)
 printf 'channel:%s:%s:%s pointer:%s:%s:%s:%s copy:%s:%s:%s:%s append:%s:%s:%s exact:%s:%s\n' "$channelLen" "$channelCap" "$closedLen" "$pointerLen" "$pointerCap" "$nilPointerLen" "$nilPointerCap" "$copied" bytes[0] bytes[1] bytes[2] appended[0] appended[1] appended[2] "$exact" "$exactNegative"
}
main()
`
	out, stderr, err := runBashSharpCall(t, src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "typed:9007199254740993:9007199254740993\n"+
		"exact-print 9007199254740993\n"+
		"channel:1:3:1 pointer:4:4:5:5 copy:3:195:169:120 append:65:195:169 exact:9007199254740993:-9007199254740993\n"))
}

func TestBashPPBuiltinStringScalarProvenance(t *testing.T) {
	const src = `type Label string
type LabelAlias = Label
func requireSame[T any](a T, b T) { printf 'typed:%s:%s\n' "$a" "$b" }
func main() {
 numeric := "10"
 numericAlias := numeric
 truth := "true"
 truthAlias := truth
 shortMin := min(numericAlias, "2")
	shortMinAlias := shortMin
	truthMin := min(truthAlias, "z")
	shortLen := len(truthAlias)
	selectedLen := len(shortMinAlias)
 copiedBytes := make([]byte, 4)
 copied := copy(copiedBytes, truthAlias)
 empty := make([]byte, 0, 2)
 appended := append(empty, numericAlias...)
 var defined LabelAlias = "20"
 definedMin := min(defined, "3")
 requireSame(definedMin, defined)
 definedLen := len(defined)
	definedEmpty := make([]byte, 0, 2)
	definedBytes := append(definedEmpty, defined...)
 printf 'short:%s:%s:%s:%s copy:%s:%s:%s:%s:%s append:%s:%s defined:%s:%s:%s:%s\n' "$shortMin" "$truthMin" "$shortLen" "$selectedLen" "$copied" copiedBytes[0] copiedBytes[1] copiedBytes[2] copiedBytes[3] appended[0] appended[1] "$definedMin" "$definedLen" definedBytes[0] definedBytes[1]
}
main()
`
	out, stderr, err := runBashSharpCall(t, src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "typed:20:20\n"+
		"short:10:true:4:2 copy:4:116:114:117:101 append:49:48 defined:20:2:50:48\n"))
}

func TestBashPPBuiltinDiagnosticsAndReadonly(t *testing.T) {
	tests := []struct{ name, body, want string }{
		{"len arity", "_ := len()", "BASHPP-EBUILTIN-ARITY:"},
		{"cap type", "_ := cap(1)", "BASHPP-EBUILTIN-TYPE:"},
		{"cap map", "m := make(map[string]int)\n _ := cap(m)", "BASHPP-EBUILTIN-TYPE:"},
		{"append type", "_ := append(1, 2)", "BASHPP-EBUILTIN-TYPE:"},
		{"copy type", "_ := copy(1, 2)", "BASHPP-EBUILTIN-TYPE:"},
		{"delete nil allowed", "var m map[string]int\n delete(m, \"x\")\n println(\"ok\")", ""},
		{"readonly clear", "s := []int{1}\n readonly s\n clear(s)", "BASHPP-EREADONLY-MUTATION:"},
		{"readonly append alias", "s := make([]int, 0, 1)\n readonly s\n _ := append(s, 1)", "BASHPP-EREADONLY-MUTATION:"},
		{"make negative", "n := -1\n _ := make([]int, n)", "BASHPP-EBUILTIN-SIZE:"},
		{"make arity", "_ := make()", "BASHPP-EBUILTIN-ARITY:"},
		{"new arity", "_ := new()", "BASHPP-EBUILTIN-TYPE:"},
		{"len untyped nil", "_ := len(nil)", "BASHPP-EBUILTIN-NIL:"},
		{"append untyped nil", "_ := append(nil, 1)", "BASHPP-EBUILTIN-NIL:"},
		{"min mixed", "_ := min(1, true)", "BASHPP-EBUILTIN-TYPE:"},
		{"copy string non-byte", "s := make([]int, 2)\n _ := copy(s, \"ab\")", "BASHPP-EBUILTIN-TYPE:"},
		{"append string non-byte", "s := make([]int, 0)\n _ := append(s, \"ab\"...)", "BASHPP-EBUILTIN-TYPE:"},
		{"append string defined byte", "type Octet byte\n s := make([]Octet, 0)\n _ := append(s, \"ab\"...)", "BASHPP-EBUILTIN-TYPE:"},
		{"min named mismatch", "type A int\n type B int\n var a A = 1\n var b B = 2\n _ := min(a, b)", "BASHPP-EBUILTIN-TYPE:"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, stderr, err := runBashSharpCall(t, "func main() {\n"+test.body+"\n}\nmain()\n")
			if test.want == "" {
				qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
				return
			}
			qt.Assert(t, qt.ErrorIs(err, interp.ExitStatus(2)))
			if !strings.HasPrefix(stderr, test.want) {
				t.Fatalf("stderr %q does not start with %q", stderr, test.want)
			}
		})
	}
}
