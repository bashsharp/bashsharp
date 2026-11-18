package lower_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/go-quicktest/qt"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// profileCase is one public Go-profile source case. The sources are the corpus
// fixtures verbatim; stderr is the exact recorded process stderr.
type profileCase struct{ id, origin, source, stderr string }

// profileRejectCases are the 15 semantic-reject rows of the public phase
// contract. cap-type-neg is the one that records two diagnostics.
var profileRejectCases = []profileCase{
	{
		id:     "assert-impossible-neg",
		origin: "assertions/assert-impossible-neg.bpp",
		source: `type T int
func (v T) M(s string) { }
type U int
type I interface { M(string) }
func main() { var v T = 1; var i I = v; x, ok := i.(U); echo $x $ok }
main()
`,
		stderr: "assertions/assert-impossible-neg.bpp: line 5: BASHPP-EASSERT-IMPOSSIBLE: U cannot be asserted from I\n",
	},
	{
		id:     "cap-type-neg",
		origin: "builtins/cap-type-neg.bpp",
		source: `func main() {
 _ := cap(1)
}
main()
`,
		stderr: "BASHPP-EBUILTIN-TYPE: cap argument must be an array or slice\nbuiltins/cap-type-neg.bpp: line 2: BASHPP-ESHORT-NONEW: no new variables on left side of :=\n",
	},
	{
		id:     "struct-literal-mixed-neg",
		origin: "composites/struct-literal-mixed-neg.bpp",
		source: `type Pair struct { A int; B int }
func main() {
 x := Pair{A: 1, 2}
}
main()
`,
		stderr: "BASHPP-ESTRUCT-MIXED: Pair literal cannot mix keyed and positional fields\n",
	},
	{
		id:     "typed-overflow-neg",
		origin: "const/typed-overflow-neg.bpp",
		source: `const (
 A int8 = iota + 127
 B
)
`,
		stderr: "const/typed-overflow-neg.bpp: line 3: BASHPP-EEXPR-CONVERT: constant 128 overflows int8\n",
	},
	{
		id:     "for-cond-nonboolean-neg",
		origin: "control-flow/for-cond-nonboolean-neg.bpp",
		source: `func main() {
	for 1 + 1 { echo wrong }
}
main()
`,
		stderr: "BASHPP-EFOR-COND: for condition must be boolean, got Int\n",
	},
	{
		id:     "if-cond-nonboolean-neg",
		origin: "control-flow/if-cond-nonboolean-neg.bpp",
		source: `func main() {
	if 1 { echo wrong }
}
main()
`,
		stderr: "BASHPP-EIF-COND: if condition must be boolean, got Int\n",
	},
	{
		id:     "range-scalar-arity-neg",
		origin: "control-flow/range-scalar-arity-neg.bpp",
		source: `func main() {
for i, v := range 2 {}
}
main()
`,
		stderr: "control-flow/range-scalar-arity-neg.bpp: line 2: BASHPP-ERANGE-ARITY: integer range permits at most one iteration variable\n",
	},
	{
		id:     "switch-case-type-neg",
		origin: "control-flow/switch-case-type-neg.bpp",
		source: `func main() { switch 1 { case "1": echo wrong } }
main()
`,
		stderr: "BASHPP-ESWITCH-TYPE: case expression type String does not match switch tag type Int\n",
	},
	{
		id:     "assign-kind-mismatch-neg",
		origin: "conversions/assign-kind-mismatch-neg.bpp",
		source: `var text string = 1
`,
		stderr: "conversions/assign-kind-mismatch-neg.bpp: line 1: BASHPP-EASSIGN-TYPE: cannot use Int constant as string in declaration\n",
	},
	{
		id:     "const-overflow-int8-neg",
		origin: "conversions/const-overflow-int8-neg.bpp",
		source: `var n int8 = 128
`,
		stderr: "conversions/const-overflow-int8-neg.bpp: line 1: BASHPP-EEXPR-CONVERT: constant 128 overflows int8\n",
	},
	{
		id:     "short-decl-no-new-neg",
		origin: "expressions/short-decl-no-new-neg.bpp",
		source: `x := 1
x := 2
`,
		stderr: "expressions/short-decl-no-new-neg.bpp: line 2: BASHPP-ESHORT-NONEW: no new variables on left side of :=\n",
	},
	{
		id:     "cannot-infer-neg",
		origin: "generics/cannot-infer-neg.bpp",
		source: `func zero[T any]() T {
 return 0
}
zero()
`,
		stderr: "BASHPP-EGENERIC-INFER: cannot infer type arguments for zero\n",
	},
	{
		id:     "constraint-violation-neg",
		origin: "generics/constraint-violation-neg.bpp",
		source: `func bad[T comparable](v T) {}
var xs []int = []int{1}
bad(xs)
`,
		stderr: "BASHPP-EGENERIC-CONSTRAINT: []int does not satisfy constraint for T in bad\n",
	},
	{
		id:     "missing-method-neg",
		origin: "interfaces/missing-method-neg.bpp",
		source: `type T int
func (v T) N(s string) { }
type I interface { M(string) }
func main() { var v T = 1; var i I = v }
main()
`,
		stderr: "BASHPP-EINTERFACE-MISSING: T does not implement interface (missing method M)\n",
	},
	{
		id:     "undefined-receiver-neg",
		origin: "methods/undefined-receiver-neg.bpp",
		source: `func (v Missing) M() { return; }
`,
		stderr: "invalid receiver type Missing (type is not declared in this session)\n",
	},
}

// profileRuntimeNegatives are the five rows the phase contract assigns to the
// artifact, not to this checker. They exit non-zero, but only after running.
var profileRuntimeNegatives = []profileCase{
	{
		id:     "assert-fail-neg",
		origin: "assertions/assert-fail-neg.bpp",
		source: `type T int
func (v T) M(s string) { }
type U int
func (v U) M(s string) { }
type I interface { M(string) }
func main() { var v T = 1; var i I = v; x := i.(U); echo $x }
main()
`,
		stderr: "BASHPP-EASSERT-FAIL: interface value has dynamic type T, not U\n",
	},
	{
		id:     "nil-deref-neg",
		origin: "pointers/nil-deref-neg.bpp",
		source: `func main() {
 var p *int
 x := *p
}
main()
`,
		stderr: "BASHPP-ENIL-DEREF: dereference of nil pointer\n",
	},
	{
		id:     "readonly-clear-neg",
		origin: "builtins/readonly-clear-neg.bpp",
		source: `func main() {
 s := []int{1}
 readonly s
 clear(s)
}
main()
`,
		stderr: "BASHPP-EREADONLY-MUTATION: cannot mutate readonly value \"s\" through clear\n",
	},
	{
		id:     "readonly-bypass-neg",
		origin: "pointers/readonly-bypass-neg.bpp",
		source: `func main() {
 x := 1
 p := &x
 readonly x
 *p = 2
}
main()
`,
		stderr: "BASHPP-EREADONLY-MUTATION: cannot mutate readonly value through pointer\n",
	},
	{
		id:     "panic-unrecovered",
		origin: "panic-unrecovered.bpp",
		source: `func f() {
 echo before
 panic(boom)
 echo after
}
f()
echo unreachable
`,
		stderr: "panic: boom\n",
	},
}

// profileValidCases are artifact-run rows chosen for the constructs this
// checker reasons about: scopes, iota repetition, conditions, switch arms,
// method sets, generic instantiation and the length builtins. The full 105-row
// sweep is available through TestCheckProfileCorpusSweep.
var profileValidCases = []profileCase{
	{
		id:     "collection-builtins",
		origin: "builtins/collection-builtins.bpp",
		source: `type Vec[T any] []T
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
`,
	},
	{
		id:     "residual-forms",
		origin: "builtins/residual-forms.bpp",
		source: `type Count uint64
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
`,
	},
	{
		id:     "string-scalar-provenance",
		origin: "builtins/string-scalar-provenance.bpp",
		source: `type Label string
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
`,
	},
	{
		id:     "collections-read-mutate",
		origin: "composites/collections-read-mutate.bpp",
		source: `func main() {
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
`,
	},
	{
		id:     "struct-copy-vs-reference",
		origin: "composites/struct-copy-vs-reference.bpp",
		source: `type Inner struct { Name string }
type Config struct { Inner Inner; Fixed [1]Inner; Ports []int; Labels map[string][]int }
func main() {
 a := Config{Inner: Inner{Name: "a"}, Fixed: [1]Inner{{Name: "fixed"}}, Ports: []int{1}, Labels: map[string][]int{"x": {2}}}
 b := a
 b.Inner.Name = "b"
 b.Fixed[0].Name = "copy"
 b.Ports[0] = 3
 b.Labels["x"][0] = 4
 printf '%s:%s:%s:%s:%s:%s:%s:%s\n' a.Inner.Name b.Inner.Name a.Fixed[0].Name b.Fixed[0].Name a.Ports[0] b.Ports[0] a.Labels["x"][0] b.Labels["x"][0]
}
main()
`,
	},
	{
		id:     "struct-literals-zero-values",
		origin: "composites/struct-literals-zero-values.bpp",
		source: `type Inner struct { Name string; Count int }
type Config struct { Inner Inner; Fixed [1]Inner; Ports []int; Labels map[string]string }
func main() {
	var zero Config
	var initialized Config = Config{Inner: Inner{Name: "decl"}}
 keyed := Config{Inner: Inner{Name: "prod"}, Fixed: [1]Inner{{Name: "fixed"}}, Ports: []int{80}, Labels: map[string]string{"tier": "edge"}}
 positional := Inner{"pos", 2}
 anon := struct{Name string}{Name: "anon"}
 keyed.Inner.Count = 4
	printf '%s:%s:%s:%s:%s:%s:%s:%s\n' zero.Inner.Name zero.Inner.Count initialized.Inner.Name keyed.Inner.Name keyed.Inner.Count keyed.Fixed[0].Name positional.Name anon.Name
}
main()
`,
	},
	{
		id:     "blank-specs-advance-iota",
		origin: "const/blank-specs-advance-iota.bpp",
		source: `const (
 _ = iota
 A
 _
 B
)
func main() { printf '%s:%s\n' "$A" "$B" }
main()
`,
	},
	{
		id:     "iota-repetition-types",
		origin: "const/iota-repetition-types.bpp",
		source: `type Count int
const (
 A = iota
 B
 C = iota + 3
 D Count = iota
 E
)
func same[T any](a T, b T) { printf 'same:%s:%s\n' "$a" "$b" }
func main() {
 same(D, E)
 printf '%s:%s:%s:%s:%s\n' "$A" "$B" "$C" "$D" "$E"
}
main()
`,
	},
	{
		id:     "iota-shadowing",
		origin: "const/iota-shadowing.bpp",
		source: `const (
 iota = iota
 A = iota
)
func main() { printf '%s:%s\n' "$iota" "$A" }
main()
`,
	},
	{
		id:     "typed-expression-identity",
		origin: "const/typed-expression-identity.bpp",
		source: `type Count int
const (
 A Count = 1
 B = A + 1
)
func same[T any](a T, b T) { printf 'same:%s:%s\n' "$a" "$b" }
func main() {
 same(A, B)
}
main()
`,
	},
	{
		id:     "branch-nested-targets",
		origin: "control-flow/branch-nested-targets.bpp",
		source: `func main() {
	for i := 0; i < 5; i++ {
		body := i
		switch i {
		case 0:
			continue
		case 1:
			fallthrough
		case 99:
			echo "fell:$body"
		case 2:
			break
		default:
			echo "body:$body"
		}
		if i == 3 { break }
		echo "tail:$i"
	}
	echo done
}
main()
`,
	},
	{
		id:     "for-assignment-init-post",
		origin: "control-flow/for-assignment-init-post.bpp",
		source: `func main() {
	i := 0
	for i = 1; i <= 3; i = i + 1 { echo $i }
	echo "after:$i"
}
main()
`,
	},
	{
		id:     "for-forms-per-iteration",
		origin: "control-flow/for-forms-per-iteration.bpp",
		source: `func main() {
	outer := 9
	for false { echo wrong }
	for i := 0; i < 3; i++ {
		defer func() { echo "capture:$i" }()
		body := i
		echo "body:$body"
	}
	echo "after:$outer:${i-unset}:${body-unset}"
	for { echo infinite; return }
}
main()
`,
	},
	{
		id:     "if-branches-scopes",
		origin: "control-flow/if-branches-scopes.bpp",
		source: `func main() {
	n := 9
	if n := 2; n > 2 {
		echo high
	} else if n == 2 {
		echo "equal:$n"
		branch := "yes"
		echo "$branch"
	} else {
		echo low
	}
	echo "after:$n:${branch-unset}"
	if false {
		echo wrong
	} else {
		echo fallback
	}
}
main()
`,
	},
	{
		id:     "range-control-flow",
		origin: "control-flow/range-control-flow.bpp",
		source: `func stop() {
 for i := range 5 {
  if i == 0 { continue }
  printf 'loop:%s\n' "$i"
  if i == 2 { return }
 }
 echo unreachable
}
func main() {
 for i := range 2 { if i == 1 { break } }
 printf 'after:<%s>\n' "${i-unset}"
 stop()
 echo caller
}
main()
`,
	},
	{
		id:     "range-strings-integers-generic",
		origin: "control-flow/range-strings-integers-generic.bpp",
		source: `type Vec[T any] []T
func main() {
 text := "aé"
 for i, r := range text { printf 'string:%s:%s\n' "$i" "$r" }
 for i := range 3 { printf 'integer:%s\n' "$i" }
 for range -2 { echo unreachable }
 values := Vec[int]{7, 8}
 for i, v := range values { printf 'generic:%s:%s\n' "$i" "$v" }
}
main()
`,
	},
	{
		id:     "switch-first-match-scopes",
		origin: "control-flow/switch-first-match-scopes.bpp",
		source: `func main() {
	n := 2
	switch x := n + 1; x {
	case 1, 3:
		seen := "first"
		echo "$seen:$x"
	case x:
		echo wrong
	default:
		echo fallback
	}
	echo "after:${x-unset}:${seen-unset}"
	switch {
	case n < 0:
		echo wrong
	case n == 2, true:
		inside := "yes"
		echo "tagless:$inside"
	default:
		echo wrong
	}
	echo "scope:${inside-unset}"
	switch n = n + 1; {
	case n == 3:
		echo "assigned:$n"
	default:
		echo wrong
	}
}
main()
`,
	},
	{
		id:     "short-decl-evaluates",
		origin: "expressions/short-decl-evaluates.bpp",
		source: `x := 42; y, z := 1, 2; printf '%s:%s:%s' "$x" "$y" "$z"`,
	},
	{
		id:     "tuple-redeclare-new-name",
		origin: "expressions/tuple-redeclare-new-name.bpp",
		source: `x := 1
x, y := 2, 3
printf '%s:%s' "$x" "$y"
`,
	},
	{
		id:     "typed-zero-values-const",
		origin: "expressions/typed-zero-values-const.bpp",
		source: `var n int
var ok bool
var text string
const K int8 = 7
printf '<%s>:<%s>:<%s>:%s' "$n" "$ok" "$text" "$K"
`,
	},
	{
		id:     "comparable-constraint",
		origin: "generics/comparable-constraint.bpp",
		source: `func same[T comparable](a, b T) T {
 return a
}
x := same[int](1, 2)
echo "$x"
`,
	},
	{
		id:     "explicit-substitution",
		origin: "generics/explicit-substitution.bpp",
		source: `func id[T any](v T) T {
 return v
}
x := id[int](7)
echo "x=$x"
`,
	},
	{
		id:     "inference-composite",
		origin: "generics/inference-composite.bpp",
		source: `func sink[T any](v T) {
 echo ok
}
var xs []int = []int{1, 2}
sink(xs)
`,
	},
	{
		id:     "inference-scalar",
		origin: "generics/inference-scalar.bpp",
		source: `func id[T any](v T) T {
 return v
}
x := id(hello)
echo "x=$x"
`,
	},
	{
		id:     "assignment-assert-type-switch",
		origin: "interfaces/assignment-assert-type-switch.bpp",
		source: `type Count int
func (v Count) Show(prefix string) { echo "$prefix:$v"; }
func (v Count) Label(n int) string { return "$n:$v"; }
type Shower interface { Show(string) }
type Labeler interface { Label(int) string }
func main() {
	var v Count = 7
	var i Shower = v
	x, ok := i.(Count)
	echo "assert:$x:$ok"
	var l Labeler = v
	label := l.Label(3)
	echo "label:$label"
	i.Show(iface)
	y := i.(Count)
	y.Show(method)
	var nilI Shower
	switch n := nilI.(type) {
	case nil:
		echo nil-interface
	default:
		echo "$n"
	}
	var p *Count = nil
	var pi Shower = p
	switch q := pi.(type) {
	case nil:
		echo wrong
	default:
		echo typed-nil-pointer:${q-unset}
	}
	switch z := i.(type) {
	case nil:
		echo wrong
	case Count:
		echo "type:$z"
	default:
		echo wrong
	}
	snap(i)
}
func snap(i Shower) {
	a, ok := i.(Count)
	echo "subshell:$a:$ok"
}
main()
`,
	},
	{
		id:     "embedded-promotion-assertions",
		origin: "interfaces/embedded-promotion-assertions.bpp",
		source: `type File struct { N int }
func (v File) Read(prefix string) { echo "read:$prefix"; }
func (p *File) Close() { p.N = 0; echo closed; }
type Reader interface { Read(string) }
type Closer interface { Close() }
type ReadCloser interface { Reader; Closer }
func main() {
	var f File = File{N: 8}
	p := &f
	var rc ReadCloser = p
	rc.Read(file)
	rc.Close()
	var r Reader = rc
	r.Read(after)
	c, ok := r.(Closer)
	echo "assert-interface:$ok"
	c.Close()
	switch x := r.(type) {
	case Closer:
		echo closer
	default:
		echo "$x"
	}
	var nilRC ReadCloser
	var nilR Reader = nilRC
	nilC, nilOK := nilR.(Closer)
	echo "nil-interface-assert:$nilC:$nilOK"
}
main()
`,
	},
	{
		id:     "method-sets-pointer-identity",
		origin: "interfaces/method-sets-pointer-identity.bpp",
		source: `type Box struct { N int }
func (v Box) Value(prefix string) { printf '%s:%s\n' "$prefix" v.N; }
func (p *Box) Set(n int) { p.N = n; }
type Valuer interface { Value(string) }
type Mutator interface { Set(int) }
func main() {
	var b Box = Box{N: 4}
	var v Valuer = b
	b.N = 9
	v.Value(copy)
	bp := &b
	var m Mutator = bp
	m.Set(12)
	printf 'mutated:%s\n' b.N
}
main()
`,
	},
	{
		id:     "value-copy-assertion-zero",
		origin: "interfaces/value-copy-assertion-zero.bpp",
		source: `type Box struct { N int }
func (v Box) Show(prefix string) { echo "$prefix:${v.N}"; }
type Shower interface { Show(string) }
type Other int
func (v Other) Show(prefix string) { echo "$prefix:$v"; }
func main() {
	var box Box = Box{N: 1}
	var i Shower = box
	box.N = 9
	stored := i.(Box)
	printf 'copy:%s\n' stored.N
	zero, ok := i.(Other)
	echo "zero:$zero:$ok"
	p := &box
	var pi Shower = p
	q := pi.(*Box)
	q.N = 12
	printf 'pointer:%s\n' box.N
}
main()
`,
	},
	{
		id:     "dispatch",
		origin: "methods/dispatch.bpp",
		source: `type Count int
func (v Count) Show(prefix string) {
 echo "$prefix:$v"
}
func (p *Count) Pointer() {
 echo "ptr:$p"
}
func relay(v Count) {
 v.Show(relay)
}
func makeCount() Count {
 return 11
}
var v Count = 7
v.Show(direct)
v.Pointer()
relay(v)
x := makeCount()
x.Show(result)
var q *Count = 9
q.Show(deref)
(*Count).Pointer(q)
f := v.Show
f(value)
pf := v.Pointer
pf()
Count.Show(v, expression)
func later() {
 defer v.Show(deferred)
}
later()
var p *Count
func (p *Count) NilOK() {
 if [ -z "$p" ]; then echo nil; fi
}
p.NilOK()
`,
	},
	{
		id:     "address-deref-new",
		origin: "pointers/address-deref-new.bpp",
		source: `type S struct { N int }
func main() {
 x := 1
 p := &x
 before := *p
 *p = 3
 s := S{N: 4}
 fp := &s.N
 *fp = 5
 a := [1]int{6}
 ap := &a[0]
 *ap = 7
 sl := []int{8}
 sp := &sl[0]
 *sp = 9
 np := new(int)
 *np = 10
 nv := *np
 ns := new(S)
 sv := *ns
 na := new([1]int)
 av := *na
 nsl := new([]int)
 slv := *nsl
 nm := new(map[string]int)
 mv := *nm
 printf '%s:%s:%s:%s:%s:%s:%s:%s:%s:%s\n' "$before" "$x" s.N a[0] sl[0] "$nv" sv.N av[0] "$slv" "$mv"
}
main()
`,
	},
	{
		id:     "subshell-snapshot",
		origin: "pointers/subshell-snapshot.bpp",
		source: `func main() {
 x := 1
 p := &x
 ( *p = 2; printf 'sub:%s\n' "$x" )
	 printf 'parent:%s\n' "$x"
}
main()
`,
	},
	{
		id:     "compound-incdec-runtime",
		origin: "expressions/compound-incdec-runtime.bpp",
		source: `type Box struct { N int }
func main() {
 var x int = 5
 x += 3
 x *= 2
 x--
 var text string = "a"
 text += "b"
 values := []int{2}
 i := 0
 values[i]+=3
 box := Box{N: 4}
 box.N++
 p := &x
 *p += 2
 printf '%s:%s:%s:%s' "$x" "$text" values[0] box.N
}
main()
`,
	},
	{
		id:     "map-index-compound-incdec",
		origin: "expressions/map-index-compound-incdec.bpp",
		source: `func main() {
 m := map[string]uint8{"present": 254}
 key := "present"
 m[key]++
 m["missing"] += 2
 printf '%s:%s' m["present"] m["missing"]
}
main()
`,
	},
	{
		id:     "constraint-approximation",
		origin: "constraint-approximation.bpp",
		source: `type Age int
func id[T ~int\|string](v T) T {
 return v
}
var a Age = 9
x := id(a)
echo "$x"
`,
	},
	{
		id:     "constraint-union",
		origin: "constraint-union.bpp",
		source: `func id[T ~int\|string](v T) T {
 return v
}
x := id[string](hi)
echo "$x"
`,
	},
	{
		id:     "generic-receiver-interface",
		origin: "generic-receiver-interface.bpp",
		source: `type Echoer interface { Echo(int) int }
type Box[T any] struct { Value T }
func (b Box[T]) Echo(v T) T { return v }
func main() {
 var b Box[int] = Box[int]{Value:1}
 var e Echoer = b
 x := e.Echo(9)
 echo "$x"
}
main()
`,
	},
	{
		id:     "literal-integer",
		origin: "literal-integer.bpp",
		source: `func main() {
 a := 0b101
 b := 0o17
 c := 0x1f
 d := 1_000
 println(a, b, c, d)
}
main()
`,
	},
	{
		id:     "explicit-conversion",
		origin: "explicit-conversion.bpp",
		source: `func main() {
	base := 40
	copy := base
	n := copy + 2
	ok := n == 42
	s := string(65)
	printf '%s:%s:%s' "$n" "$ok" "$s"
}
main()
`,
	},
	{
		id:     "constant-beyond-int64",
		origin: "constant-beyond-int64.bpp",
		source: `func main() {
	n := 9223372036854775808 + 1
	printf '%s' "$n"
}
main()
`,
	},
	{
		id:     "closure-parameter",
		origin: "closure-parameter.bpp",
		source: `func apply(cb func, n int) {
 cb($n)
}
show := func(v int) {
 echo "v=$v"
}
apply($show, 4)
`,
	},
}

func parseProfile(t *testing.T, source, origin string) *syntax.File {
	t.Helper()
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(source), origin)
	qt.Assert(t, qt.IsNil(err))
	return file
}

// renderProfile is the rendering a product formatter performs: one diagnostic
// per line, in the order the checker returned them.
func renderProfile(list lower.ErrorList) string {
	var b strings.Builder
	for _, d := range list {
		b.WriteString(d.Error())
		b.WriteString("\n")
	}
	return b.String()
}

// runInterpreter executes source with the real interpreter and returns its
// stderr and exit status. It is the oracle for every expectation below: no test
// here asserts a message this repository's interpreter does not itself print.
func runInterpreter(t *testing.T, source, origin string) (string, int) {
	t.Helper()
	var out, stderr bytes.Buffer
	runner, err := interp.New(
		interp.Lang(syntax.LangBashPP),
		interp.WithBashCompatErrors(true),
		interp.StdIO(nil, &out, &stderr),
		interp.Dir(t.TempDir()),
		interp.Env(expand.ListEnviron("PATH=/no-tools")),
	)
	qt.Assert(t, qt.IsNil(err))
	status := 0
	if err := runner.Run(context.Background(), parseProfile(t, source, origin)); err != nil {
		var exit interp.ExitStatus
		if !errors.As(err, &exit) {
			t.Fatalf("run %s: %v", origin, err)
		}
		status = int(exit)
	}
	return stderr.String(), status
}

// TestCheckProfileMatchesSourceDiagnostics is the certified slice: for each of
// the 15 semantic-reject cases the checker must produce the recorded stderr
// bytes exactly, including which diagnostics carry an origin/line prefix, the
// one diagnostic that carries no code at all, and the order of the two
// diagnostics cap-type-neg records.
func TestCheckProfileMatchesSourceDiagnostics(t *testing.T) {
	qt.Assert(t, qt.Equals(len(profileRejectCases), 15))
	for _, tc := range profileRejectCases {
		t.Run(tc.id, func(t *testing.T) {
			file := parseProfile(t, tc.source, tc.origin)
			got := renderProfile(lower.CheckProfile(file, tc.origin))
			qt.Assert(t, qt.Equals(got, tc.stderr))

			// The recorded bytes are not taken on trust: the same source is run
			// through the interpreter in this checkout and must agree.
			stderr, status := runInterpreter(t, tc.source, tc.origin)
			qt.Assert(t, qt.Equals(stderr, tc.stderr))
			qt.Assert(t, qt.Equals(status, 2))
		})
	}
}

// TestCheckProfileRuntimeNegativesAreNotStatic pins the phase split. These five
// programs are rejected by the interpreter, but only after they have run, so
// classifying them here would claim a decision this checker cannot make.
func TestCheckProfileRuntimeNegativesAreNotStatic(t *testing.T) {
	qt.Assert(t, qt.Equals(len(profileRuntimeNegatives), 5))
	for _, tc := range profileRuntimeNegatives {
		t.Run(tc.id, func(t *testing.T) {
			file := parseProfile(t, tc.source, tc.origin)
			qt.Assert(t, qt.IsNil(lower.CheckProfile(file, tc.origin)))

			// They are genuinely rejected — just not here.
			stderr, status := runInterpreter(t, tc.source, tc.origin)
			qt.Assert(t, qt.Equals(stderr, tc.stderr))
			qt.Assert(t, qt.Equals(status, 2))
		})
	}
}

// TestCheckProfileAcceptsValidPrograms is the false-positive gate.
func TestCheckProfileAcceptsValidPrograms(t *testing.T) {
	for _, tc := range profileValidCases {
		t.Run(tc.id, func(t *testing.T) {
			file := parseProfile(t, tc.source, tc.origin)
			if list := lower.CheckProfile(file, tc.origin); list != nil {
				t.Fatalf("valid program rejected:\n%s", renderProfile(list))
			}
		})
	}
}

// TestCheckProfileIsStructural renames every identifier and moves every
// declaration, so nothing a fixture-name table or a memorised message could
// match survives. Each expectation is the interpreter's own stderr for the
// rewritten program, which is what makes this a rule test rather than a
// restatement of the corpus.
func TestCheckProfileIsStructural(t *testing.T) {
	renamed := []profileCase{{
		id:     "assert-impossible/renamed",
		origin: "renamed/assert.bpp",
		source: `type Alpha int
func (value Alpha) Ping(text string) { }
type Beta int
type Pinger interface { Ping(string) }
func run() { var value Alpha = 1; var boxed Pinger = value; got, ok := boxed.(Beta); echo $got $ok }
run()
`,
	}, {
		id:     "cap-type/renamed",
		origin: "renamed/cap.bpp",
		source: `func run() {
 echo warmup
 _ := cap(97)
}
run()
`,
	}, {
		id:     "struct-mixed/renamed",
		origin: "renamed/struct.bpp",
		source: `type Point struct { X int; Y int }
func run() {
 here := Point{X: 3, 4}
}
run()
`,
	}, {
		id:     "typed-overflow/renamed",
		origin: "renamed/const.bpp",
		source: `const (
 First int16 = iota + 32767
 Second
)
`,
	}, {
		id:     "for-cond/renamed",
		origin: "renamed/for.bpp",
		source: `func run() {
	for 2 * 3 { echo wrong }
}
run()
`,
	}, {
		id:     "if-cond/renamed",
		origin: "renamed/if.bpp",
		source: `func run() {
	echo warmup
	if 40 + 2 { echo wrong }
}
run()
`,
	}, {
		id:     "range-arity/renamed",
		origin: "renamed/range.bpp",
		source: `func run() {
echo warmup
for index, element := range 5 {}
}
run()
`,
	}, {
		id:     "switch-case-type/renamed",
		origin: "renamed/switch.bpp",
		source: `func run() { switch 9 { case "nine": echo wrong } }
run()
`,
	}, {
		id:     "assign-kind/renamed",
		origin: "renamed/assign.bpp",
		source: `echo warmup
var greeting string = 7
`,
	}, {
		id:     "const-overflow/renamed",
		origin: "renamed/overflow.bpp",
		source: `echo warmup
echo warmup
var small int16 = 40000
`,
	}, {
		id:     "short-nonew/renamed",
		origin: "renamed/short.bpp",
		source: `count := 1
echo warmup
count := 2
`,
	}, {
		id:     "cannot-infer/renamed",
		origin: "renamed/infer.bpp",
		source: `func blank[Element any]() Element {
 return 0
}
blank()
`,
	}, {
		id:     "constraint/renamed",
		origin: "renamed/constraint.bpp",
		source: `func needsKey[Key comparable](v Key) {}
var items []string = []string{"a"}
needsKey(items)
`,
	}, {
		id:     "missing-method/renamed",
		origin: "renamed/missing.bpp",
		source: `type Widget int
func (w Widget) Other(s string) { }
type Drawer interface { Draw(string) }
func run() { var w Widget = 1; var d Drawer = w }
run()
`,
	}, {
		id:     "undefined-receiver/renamed",
		origin: "renamed/receiver.bpp",
		source: `func (r Absent) Go() { return; }
`,
	}}
	qt.Assert(t, qt.Equals(len(renamed), len(profileRejectCases)))
	for _, tc := range renamed {
		t.Run(tc.id, func(t *testing.T) {
			want, status := runInterpreter(t, tc.source, tc.origin)
			qt.Assert(t, qt.Equals(status, 2))
			qt.Assert(t, qt.Not(qt.Equals(want, "")))
			file := parseProfile(t, tc.source, tc.origin)
			qt.Assert(t, qt.Equals(renderProfile(lower.CheckProfile(file, tc.origin)), want))
		})
	}
}

// TestCheckProfileNearMissControls are programs one token away from a rejected
// one. They must stay accepted, which is what separates a rule from a pattern
// that happens to fire on the corpus.
func TestCheckProfileNearMissControls(t *testing.T) {
	controls := []profileCase{
		{id: "boolean if condition", source: "func run() {\n if true { echo ok }\n}\nrun()\n"},
		{id: "boolean for condition", source: "func run() {\n for i := 0; i < 2; i++ { echo $i }\n}\nrun()\n"},
		{id: "comparison for condition", source: "func run() {\n n := 3\n for n > 0 { n = n - 1 }\n}\nrun()\n"},
		{id: "matching switch kinds", source: "func run() { switch 1 { case 2: echo x } }\nrun()\n"},
		{id: "numeric switch kinds mix", source: "func run() { switch 1 { case 1.5: echo x } }\nrun()\n"},
		{id: "in-range typed constant", source: "var n int8 = 127\n"},
		{id: "in-range grouped constant", source: "const (\n A int8 = iota + 126\n B\n)\n"},
		{id: "shadowing inner scope", source: "func run() {\n x := 1\n if true { x := 2; echo $x }\n}\nrun()\n"},
		{id: "tuple with one new name", source: "x, y := 1, 2\nx, z := 3, 4\necho $y$z\n"},
		{id: "single range variable", source: "func run() {\n for i := range 3 { echo $i }\n}\nrun()\n"},
		{id: "len of a string literal", source: "func run() {\n n := len(\"abc\")\n echo $n\n}\nrun()\n"},
		{id: "len of a string variable", source: "func run() {\n s := \"abc\"\n n := len(s)\n echo $n\n}\nrun()\n"},
		{id: "keyed struct literal", source: "type Pair struct { A int; B int }\nfunc run() {\n x := Pair{A: 1, B: 2}\n}\nrun()\n"},
		{id: "positional struct literal", source: "type Pair struct { A int; B int }\nfunc run() {\n x := Pair{1, 2}\n}\nrun()\n"},
		{id: "inferable type parameter", source: "func id[T any](v T) T {\n return v\n}\nx := id(7)\necho $x\n"},
		{id: "comparable type argument", source: "func same[T comparable](a, b T) T {\n return a\n}\nx := same[int](1, 2)\necho $x\n"},
		{id: "implemented interface", source: "type T int\nfunc (v T) M(s string) { }\ntype I interface { M(string) }\nfunc run() { var v T = 1; var i I = v }\nrun()\n"},
		{id: "possible assertion", source: "type T int\nfunc (v T) M(s string) { }\ntype I interface { M(string) }\nfunc run() { var v T = 1; var i I = v; x, ok := i.(T); echo $x $ok }\nrun()\n"},
		{id: "declared receiver", source: "type Present int\nfunc (p Present) M() { return; }\n"},
	}
	for _, tc := range controls {
		t.Run(tc.id, func(t *testing.T) {
			origin := "control.bpp"
			file := parseProfile(t, tc.source, origin)
			if list := lower.CheckProfile(file, origin); list != nil {
				t.Fatalf("control rejected:\n%s", renderProfile(list))
			}
			// The control is only a control if the interpreter accepts it too.
			stderr, status := runInterpreter(t, tc.source, origin)
			qt.Assert(t, qt.Equals(stderr, ""))
			qt.Assert(t, qt.Equals(status, 0))
		})
	}
}

// TestCheckProfileOriginRendering covers the two renderings the source surface
// uses. The prefix is not a property of the code, so it cannot be reconstructed
// downstream from Code and Msg alone.
func TestCheckProfileOriginRendering(t *testing.T) {
	const source = "x := 1\nx := 2\n"
	for _, origin := range []string{"a.bpp", "deep/nested/name.bpp", "x := 1"} {
		list := lower.CheckProfile(parseProfile(t, source, origin), origin)
		qt.Assert(t, qt.Equals(len(list), 1))
		qt.Assert(t, qt.Equals(list[0].Code, lower.CodeProfileShortNoNew))
		qt.Assert(t, qt.Equals(list[0].Pos.Line(), uint(2)))
		qt.Assert(t, qt.Equals(list[0].Error(), origin+": line 2: BASHPP-ESHORT-NONEW: no new variables on left side of :="))
	}
	// An empty origin renders "bash", which is the interpreter's own fallback.
	list := lower.CheckProfile(parseProfile(t, source, ""), "")
	qt.Assert(t, qt.Equals(list[0].Error(), "bash: line 2: BASHPP-ESHORT-NONEW: no new variables on left side of :="))

	// An unprefixed diagnostic keeps its exact text whatever the origin is.
	const unprefixed = "func run() { switch 1 { case \"1\": echo wrong } }\nrun()\n"
	list = lower.CheckProfile(parseProfile(t, unprefixed, "whatever.bpp"), "whatever.bpp")
	qt.Assert(t, qt.Equals(len(list), 1))
	qt.Assert(t, qt.Equals(list[0].Error(), "BASHPP-ESWITCH-TYPE: case expression type String does not match switch tag type Int"))
}

// TestCheckProfileUncodedDiagnostic pins the one certified diagnostic with no
// BASHPP code. Inventing one to make the shape uniform would change the bytes
// the source surface prints.
func TestCheckProfileUncodedDiagnostic(t *testing.T) {
	const source = "func (v Missing) M() { return; }\n"
	list := lower.CheckProfile(parseProfile(t, source, "m.bpp"), "m.bpp")
	qt.Assert(t, qt.Equals(len(list), 1))
	qt.Assert(t, qt.Equals(list[0].Code, ""))
	qt.Assert(t, qt.Equals(list[0].Msg, "invalid receiver type Missing (type is not declared in this session)"))
	qt.Assert(t, qt.Equals(list[0].Error(), "invalid receiver type Missing (type is not declared in this session)"))
}

// TestCheckProfileSecondaryOrder pins the one case with two diagnostics. They
// are returned in emission order, which is not position order: the operand
// diagnostic sits at a larger column than the declaration one that follows it.
func TestCheckProfileSecondaryOrder(t *testing.T) {
	const origin = "builtins/cap-type-neg.bpp"
	list := lower.CheckProfile(parseProfile(t, "func main() {\n _ := cap(1)\n}\nmain()\n", origin), origin)
	qt.Assert(t, qt.Equals(len(list), 2))
	qt.Assert(t, qt.Equals(list[0].Code, lower.CodeProfileBuiltinType))
	qt.Assert(t, qt.Equals(list[1].Code, lower.CodeProfileShortNoNew))
	qt.Assert(t, qt.IsTrue(list[0].Pos.Col() > list[1].Pos.Col()))
}

// TestCheckProfileRunsNoEffects checks the obvious but load-bearing property:
// the check is a tree walk, so a program full of side effects leaves none.
func TestCheckProfileRunsNoEffects(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")
	source := "echo touched > " + marker + "\n" +
		"mkdir " + filepath.Join(dir, "made") + "\n" +
		"export PROFILE_CHECK_RAN=yes\n" +
		"x := 1\nx := 2\n"
	origin := "effects.bpp"
	list := lower.CheckProfile(parseProfile(t, source, origin), origin)
	qt.Assert(t, qt.Equals(len(list), 1))
	_, err := os.Stat(marker)
	qt.Assert(t, qt.IsTrue(os.IsNotExist(err)))
	_, err = os.Stat(filepath.Join(dir, "made"))
	qt.Assert(t, qt.IsTrue(os.IsNotExist(err)))
	qt.Assert(t, qt.Equals(os.Getenv("PROFILE_CHECK_RAN"), ""))
	entries, err := os.ReadDir(dir)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(len(entries), 0))
}

// TestCheckProfileNilFile keeps the entry point total.
func TestCheckProfileNilFile(t *testing.T) {
	qt.Assert(t, qt.IsNil(lower.CheckProfile(nil, "x.bpp")))
}

// TestCheckProfileFacts covers the session-declaration surface: a receiver
// whose type was declared earlier in the session is not undefined, and facts
// never manufacture a rejection on their own.
func TestCheckProfileFacts(t *testing.T) {
	const source = "func (v Earlier) M() { return; }\n"
	origin := "facts.bpp"
	file := parseProfile(t, source, origin)
	qt.Assert(t, qt.Equals(len(lower.CheckProfile(file, origin)), 1))

	facts := &lower.ProfileFacts{Types: map[string]syntax.BashPPTypeExpr{"Earlier": nil}}
	qt.Assert(t, qt.IsNil(lower.CheckProfileWithFacts(file, origin, facts)))

	// Method facts complete a method set that the file alone does not carry.
	const assign = "type Local int\ntype I interface { M(string) }\nfunc run() { var v Local = 1; var i I = v }\nrun()\n"
	assigned := parseProfile(t, assign, origin)
	qt.Assert(t, qt.Equals(len(lower.CheckProfile(assigned, origin)), 1))
	withMethod := &lower.ProfileFacts{Methods: map[string][]lower.ProfileMethod{"Local": {{Name: "M"}}}}
	qt.Assert(t, qt.IsNil(lower.CheckProfileWithFacts(assigned, origin, withMethod)))
}

// TestCheckProfileCorpusSweep runs the whole public corpus when it is present.
// It is opt-in because the corpus is a separate repository; the embedded cases
// above are what CI always runs.
func TestCheckProfileCorpusSweep(t *testing.T) {
	root := os.Getenv("BASHPP_PROFILE_CORPUS")
	if root == "" {
		t.Skip("set BASHPP_PROFILE_CORPUS to the public corpus checkout")
	}
	phases, err := os.ReadFile(filepath.Join(root, "docs/lowering/go-profile-phases.tsv"))
	qt.Assert(t, qt.IsNil(err))
	phase := map[string]string{}
	for _, line := range strings.Split(string(phases), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) < 5 || strings.HasPrefix(line, "#") || fields[0] == "id" {
			continue
		}
		phase[fields[0]] = fields[1]
	}
	rejects := 0
	for _, table := range []struct{ tsv, dir string }{
		{"docs/lowering/go-profile-cases.tsv", "tests/lowering/go-profile"},
		{"docs/lowering/profile-additional.tsv", "tests/lowering/profile-additional"},
	} {
		body, err := os.ReadFile(filepath.Join(root, table.tsv))
		qt.Assert(t, qt.IsNil(err))
		for _, line := range strings.Split(string(body), "\n") {
			fields := strings.Split(line, "\t")
			if len(fields) < 7 || strings.HasPrefix(line, "#") || fields[0] == "id" {
				continue
			}
			id, fixture := fields[0], fields[2]
			wantErr, err := unquoteTSV(fields[5])
			qt.Assert(t, qt.IsNil(err))
			source, err := os.ReadFile(filepath.Join(root, table.dir, fixture))
			qt.Assert(t, qt.IsNil(err))
			origin := fixture
			if table.dir != "tests/lowering/go-profile" {
				origin = filepath.Base(fixture)
			}
			got := renderProfile(lower.CheckProfile(parseProfile(t, string(source), origin), origin))
			if phase[id] == "semantic-reject" {
				rejects++
				if got != wantErr {
					t.Errorf("%s: got %q want %q", id, got, wantErr)
				}
				continue
			}
			if got != "" {
				t.Errorf("%s: artifact-run row rejected: %q", id, got)
			}
		}
	}
	qt.Assert(t, qt.Equals(rejects, 15))
}

func unquoteTSV(field string) (string, error) {
	return strconv.Unquote(field)
}

// TestDiagnosticText is the focused test for the optional render field added to
// lower.Diagnostic. Text is additive: a diagnostic without one renders exactly
// as it always has, and one with a Text renders those bytes verbatim rather
// than a reconstruction from Code, Msg and Pos.
func TestDiagnosticText(t *testing.T) {
	pos := parseProfile(t, "x := 1\nx := 2\n", "d.bpp").Stmts[1].Pos()

	plain := lower.Diagnostic{Code: lower.CodeType, Msg: "boom", Pos: pos}
	qt.Assert(t, qt.Equals(plain.Error(), "2:1: LOWER-ETYPE: boom"))

	// A prefixed source rendering: not derivable from Code and Msg, because
	// whether the prefix appears is a per-diagnostic fact.
	prefixed := lower.Diagnostic{Code: lower.CodeProfileShortNoNew, Msg: "no new variables on left side of :=", Pos: pos,
		Text: "d.bpp: line 2: BASHPP-ESHORT-NONEW: no new variables on left side of :="}
	qt.Assert(t, qt.Equals(prefixed.Error(), prefixed.Text))
	qt.Assert(t, qt.Equals(prefixed.Code, lower.CodeProfileShortNoNew))
	qt.Assert(t, qt.Equals(prefixed.Msg, "no new variables on left side of :="))

	// An uncoded source rendering stays uncoded rather than borrowing a code.
	uncoded := lower.Diagnostic{Msg: "invalid receiver type Missing (type is not declared in this session)", Pos: pos,
		Text: "invalid receiver type Missing (type is not declared in this session)"}
	qt.Assert(t, qt.Equals(uncoded.Error(), uncoded.Text))

	// ErrorList keeps joining whatever each element renders.
	list := lower.ErrorList{plain, prefixed}
	qt.Assert(t, qt.Equals(list.Error(), plain.Error()+"\n"+prefixed.Error()))

	// Every diagnostic this checker returns carries all four, so a consumer may
	// pick either rendering.
	for _, d := range lower.CheckProfile(parseProfile(t, "func main() {\n _ := cap(1)\n}\nmain()\n", "c.bpp"), "c.bpp") {
		qt.Assert(t, qt.Not(qt.Equals(d.Text, "")))
		qt.Assert(t, qt.Not(qt.Equals(d.Msg, "")))
		qt.Assert(t, qt.Not(qt.Equals(d.Node, "")))
		qt.Assert(t, qt.IsTrue(d.Pos.IsValid()))
		qt.Assert(t, qt.IsTrue(strings.HasSuffix(d.Text, d.Msg)))
	}
}

// TestCheckProfilePromotedMethods is a regression for concluding "missing
// method" from a direct-method set. A struct that embeds a type promotes its
// methods, so a partial walk of that graph can only ever be wrong in the
// rejecting direction. Each program runs and prints 9.
func TestCheckProfilePromotedMethods(t *testing.T) {
	cases := []profileCase{{
		id: "embedded concrete value",
		source: `type Base struct { N int }
func (b Base) Read() int { return 9 }
type Reader interface { Read() int }
type Envelope struct { Base }
func main() {
 var item Envelope = Envelope{Base: Base{N: 9}}
 var reader Reader = item
 answer := reader.Read(); println(answer)
}
main()
`,
	}, {
		id: "embedded interface",
		source: `type Reader interface { Read() int }
type Base struct { N int }
func (b Base) Read() int { return 9 }
type Envelope struct { Reader }
func main() {
 var base Base = Base{N: 9}
 var inner Reader = base
 var item Envelope = Envelope{Reader: inner}
 var outer Reader = item
 answer := outer.Read(); println(answer)
}
main()
`,
	}, {
		id: "embedded pointer with pointer receiver",
		source: `type Base struct { N int }
func (b *Base) Read() int { return 9 }
type Reader interface { Read() int }
type Envelope struct { *Base }
func main() {
 b := Base{N: 9}
 pointer := &b
 var item Envelope = Envelope{Base: pointer}
 var reader Reader = item
 answer := reader.Read(); println(answer)
}
main()
`,
	}}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			const origin = "promotion.bpp"
			if list := lower.CheckProfile(parseProfile(t, tc.source, origin), origin); list != nil {
				t.Fatalf("valid promotion rejected:\n%s", renderProfile(list))
			}
			stderr, status := runInterpreter(t, tc.source, origin)
			qt.Assert(t, qt.Equals(stderr, ""))
			qt.Assert(t, qt.Equals(status, 0))
		})
	}
}

// TestCheckProfileMutableValuesAreNotConstants is a regression for reading a
// declaration's initializer as the value a later read observes. Both writes
// below land after the initializer, and the second one arrives through an alias
// that the assignment statement never names — which is why the repair is to
// stop treating a written-to name as constant at all rather than to invalidate
// direct assignment targets.
func TestCheckProfileMutableValuesAreNotConstants(t *testing.T) {
	cases := []profileCase{{
		id: "reassigned before use",
		source: `func main() {
 n := 128
 n = 1
 var tiny int8 = n
 println(tiny)
}
main()
`,
	}, {
		id: "written through an alias",
		source: `func main() {
 n := 128
 pointer := &n
 *pointer = 1
 var tiny int8 = n
 println(tiny)
}
main()
`,
	}, {
		id: "compound update before use",
		source: `func main() {
 n := 128
 n -= 127
 var tiny int8 = n
 println(tiny)
}
main()
`,
	}}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			const origin = "mutable.bpp"
			if list := lower.CheckProfile(parseProfile(t, tc.source, origin), origin); list != nil {
				t.Fatalf("mutable value read as a constant:\n%s", renderProfile(list))
			}
			stderr, status := runInterpreter(t, tc.source, origin)
			qt.Assert(t, qt.Equals(stderr, ""))
			qt.Assert(t, qt.Equals(status, 0))
		})
	}

	// A name the file never writes to is still a usable constant fact, so the
	// repair costs precision only where a write exists.
	const rejected = "func main() {\n n := 128\n var tiny int8 = n\n}\nmain()\n"
	list := lower.CheckProfile(parseProfile(t, rejected, "mutable.bpp"), "mutable.bpp")
	qt.Assert(t, qt.Equals(len(list), 1))
	qt.Assert(t, qt.Equals(list[0].Code, lower.CodeProfileExprConvert))
}

// TestCheckProfileMalformedOperandsAreUndecided pins that an operator its
// operands do not support leaves the expression undecided. go/constant panics
// rather than erroring on those, so the kinds are checked before the operation.
func TestCheckProfileMalformedOperandsAreUndecided(t *testing.T) {
	for _, tc := range []struct {
		name, source, rejectedVar string
	}{
		{"boolean-addition", "func main() { x := true + false }\nmain()\n",
			"func main() { var x bool = true + false }\nmain()\n"},
		{"string-subtraction", "func main() { x := \"left\" - \"right\" }\nmain()\n",
			"func main() { var x string = \"left\" - \"right\" }\nmain()\n"},
		{"remainder-by-zero", "func main() { x := 1 % 0 }\nmain()\n",
			"func main() { var x int = 1 % 0 }\nmain()\n"},
		{"mixed-comparison", "func main() { if \"a\" < 1 { echo wrong } }\nmain()\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const origin = "operands.bpp"
			// Sprint 198 reserves var continuations: these unsupported var
			// forms diagnose instead of falling back to shell commands. Use
			// the explicit := expression route to reach the profile evaluator.
			if tc.rejectedVar != "" {
				_, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(tc.rejectedVar), origin)
				qt.Assert(t, qt.IsNotNil(err))
				qt.Assert(t, qt.Equals(err.Error(), "operands.bpp:1:15: invalid var statement; use command var to invoke a shell command"))
			}
			file := parseProfile(t, tc.source, origin)
			binaryExpressions := 0
			syntax.Walk(file, func(node syntax.Node) bool {
				if _, ok := node.(*syntax.BashPPBinaryExpr); ok {
					binaryExpressions++
				}
				return true
			})
			qt.Assert(t, qt.Equals(binaryExpressions, 1))
			var panicked any
			var diagnostics lower.ErrorList
			func() {
				defer func() { panicked = recover() }()
				diagnostics = lower.CheckProfile(file, origin)
			}()
			qt.Assert(t, qt.IsNil(panicked))
			qt.Assert(t, qt.IsNil(diagnostics))
		})
	}
}

// profileBoundaryCases are the supplemental static rejections of the C6
// negative-boundary set. They are NOT part of the 120-row public phase
// contract and do not change it; they extend what this checker decides.
//
// Every expectation is the full stderr this checkout's interpreter produces,
// so none of it is a rewrite of a LOWER-ETYPE or go/types sentence — those say
// something different for each of these programs.
var profileBoundaryCases = []profileCase{{
	id:     "GenericTypeSetInterfaceIsNotValueType",
	origin: "input.bpp",
	source: "type Integer interface { ~int }\nvar value Integer\n",
	stderr: "input.bpp: line 2: BASHPP-EINTERFACE-TYPESET: constraint interface cannot be used as a value type\n",
}, {
	id:     "GenericDuplicateTypeParameters_Func",
	origin: "input.bpp",
	source: "func f[T any, T any](v T) T {\n return v\n}\n",
	stderr: "BASHPP-EGENERIC-PARAM: type parameter T redeclared\n",
}, {
	id:     "GenericDuplicateTypeParameters_Struct",
	origin: "input.bpp",
	source: "type Box[T any, T any] struct { Value T }\n",
	stderr: "BASHPP-EGENERIC-PARAM: type parameter T redeclared\n",
}, {
	id:     "GenericRepresentationCycles_Direct",
	origin: "input.bpp",
	source: "type Direct[T any] Direct[T]\n",
	stderr: "input.bpp: line 1: cyclic type declaration: Direct\n",
}, {
	id:     "GenericRepresentationCycles_Array",
	origin: "input.bpp",
	source: "type Array[T any] [1]Array[T]\n",
	stderr: "input.bpp: line 1: cyclic type declaration: Array\n",
}, {
	id:     "GenericRepresentationCycles_Struct",
	origin: "input.bpp",
	source: "type Struct[T any] struct { Next Struct[T] }\n",
	stderr: "input.bpp: line 1: cyclic type declaration: Struct\n",
}, {
	id:     "StructSelectorKey_DottedKey",
	origin: "input.bpp",
	source: "type Leaf struct { X int }\ntype Outer struct { Leaf }\nfunc main() {\n bad := Outer{Leaf.X: 1}\n printf '%s' \"$bad\"\n}\nmain()\n",
	stderr: "input.bpp: line 4: BASHPP-ESTRUCT-KEY: Outer literal field key must be an identifier, not a selector expression\n",
}}

// TestCheckProfileNegativeBoundaries pins the supplemental slice to the exact
// stderr bytes, which again are not uniform: two of the four identities are
// prefixed, one carries no code, and the interpreter is the oracle for all of
// them rather than the lowering type checker.
func TestCheckProfileNegativeBoundaries(t *testing.T) {
	for _, tc := range profileBoundaryCases {
		t.Run(tc.id, func(t *testing.T) {
			file := parseProfile(t, tc.source, tc.origin)
			qt.Assert(t, qt.Equals(renderProfile(lower.CheckProfile(file, tc.origin)), tc.stderr))

			stderr, status := runInterpreter(t, tc.source, tc.origin)
			qt.Assert(t, qt.Equals(stderr, tc.stderr))
			qt.Assert(t, qt.Equals(status, 2))

			// A statically rejected program produces no Go. Wiring the check
			// into the compile hook is the core's, so this asserts only that
			// today's Compile already emits nothing for these sources.
			res, err := lower.Compile(parseProfile(t, tc.source, tc.origin), lower.Options{Origin: tc.origin})
			qt.Assert(t, qt.IsNil(res))
			qt.Assert(t, qt.IsNotNil(err))
		})
	}
}

// TestCheckProfileBoundaryControls are the valid neighbours of each supplemental
// rule. A slice, map or pointer is an indirection, so a type may name itself
// through one; a constraint interface is legal where a constraint is wanted;
// distinct type parameters and identifier keys are ordinary.
func TestCheckProfileBoundaryControls(t *testing.T) {
	controls := []profileCase{
		{id: "recursive slice", source: "type List[T any] []List[T]\nvar v List[int]\necho ok\n"},
		{id: "recursive map", source: "type Tree[T any] map[string]Tree[T]\nvar v Tree[int]\necho ok\n"},
		{id: "recursive pointer", source: "type Node[T any] *Node[T]\nvar v Node[int]\necho ok\n"},
		{id: "recursive slice non-generic", source: "type L []L\nvar v L\necho ok\n"},
		{id: "constraint used as a constraint", source: "type Integer interface { ~int }\nfunc id[T Integer](v T) T { return v }\nx := id(3)\necho $x\n"},
		{id: "method interface as a value", source: "type R interface { Read() int }\nvar value R\necho ok\n"},
		{id: "distinct type parameters", source: "func f[T any, U any](v T) T {\n return v\n}\necho ok\n"},
		{id: "identifier field key", source: "type Leaf struct { X int }\nfunc main() {\n g := Leaf{X: 1}\n printf '%s' g.X\n}\nmain()\n"},
		{id: "embedded field key", source: "type Leaf struct { X int }\ntype Outer struct { Leaf }\nfunc main() {\n g := Outer{Leaf: Leaf{X: 1}}\n printf '%s' g.X\n}\nmain()\n"},
	}
	for _, tc := range controls {
		t.Run(tc.id, func(t *testing.T) {
			const origin = "control.bpp"
			if list := lower.CheckProfile(parseProfile(t, tc.source, origin), origin); list != nil {
				t.Fatalf("valid boundary neighbour rejected:\n%s", renderProfile(list))
			}
			stderr, status := runInterpreter(t, tc.source, origin)
			qt.Assert(t, qt.Equals(stderr, ""))
			qt.Assert(t, qt.Equals(status, 0))
		})
	}
}

// TestCheckProfileCycleIsStructural renames and reshapes each cycle so nothing
// a memorised type name could match survives, and checks the indirection rule
// at a remove: a cycle that passes through a pointer is legal however long it
// is, and one that does not is rejected however it is spelled.
func TestCheckProfileCycleIsStructural(t *testing.T) {
	rejected := []profileCase{
		{id: "renamed direct", source: "type Alias[E any] Alias[E]\n", stderr: "c.bpp: line 1: cyclic type declaration: Alias\n"},
		{id: "mutual through arrays", source: "type Ping struct { P [2]Pong }\ntype Pong struct { Q Ping }\n", stderr: "c.bpp: line 1: cyclic type declaration: Ping\n"},
		{id: "nested struct field", source: "type Holder struct { Inner Wrapper }\ntype Wrapper struct { Back Holder }\n", stderr: "c.bpp: line 1: cyclic type declaration: Holder\n"},
	}
	for _, tc := range rejected {
		t.Run(tc.id, func(t *testing.T) {
			const origin = "c.bpp"
			qt.Assert(t, qt.Equals(renderProfile(lower.CheckProfile(parseProfile(t, tc.source, origin), origin)), tc.stderr))
		})
	}
	accepted := []string{
		"type Ping struct { P *Pong }\ntype Pong struct { Q Ping }\n",
		"type Ring struct { Next []Ring }\n",
		"type Chain struct { Next map[string]Chain }\n",
		"type Deep struct { A [2][3]*Deep }\n",
	}
	for _, source := range accepted {
		const origin = "c.bpp"
		if list := lower.CheckProfile(parseProfile(t, source, origin), origin); list != nil {
			t.Fatalf("indirected recursion rejected:\n%s\n%s", source, renderProfile(list))
		}
	}
	// An unresolvable type leaves the walk undecided rather than clean.
	const undecided = "type Partial struct { Next Elsewhere }\n"
	qt.Assert(t, qt.IsNil(lower.CheckProfile(parseProfile(t, undecided, "c.bpp"), "c.bpp")))
}

// Independent method parameters belong to the method, not its receiver or
// another declaration with the same method name. These controls also exercise
// alias resolution and method-expression argument alignment.
func TestCheckProfileGenericMethodDiagnostics(t *testing.T) {
	cases := []struct{ name, source, want string }{
		{"renamed_arity", "type Vessel int\nfunc (v Vessel) Choose[A any, B any](a A, b B) A { return a; }\nvar owner Vessel = 1\nowner.Choose[int](7, 8)\n", "BASHPP-EGENERIC-ARITY: Choose expects 2 type argument(s); got 1\n"},
		{"renamed_nongeneric", "type Vessel int\nfunc (v Vessel) Choose(a int) int { return a; }\nvar owner Vessel = 1\nowner.Choose[int](7)\n", "BASHPP-EGENERIC-ARITY: Choose is not generic; got 1 type argument(s)\n"},
		{"renamed_value", "type Vessel int\nfunc (v Vessel) Choose[A any](a A) A { return a; }\nvar owner Vessel = 1\nf := owner.Choose\n", "BASHPP-EGENERIC-INFER: cannot infer type arguments for Choose\n"},
		{"explicit_constraint", "type Vessel int\nfunc (v Vessel) Choose[A comparable](a A) A { return a; }\nvar owner Vessel = 1\nvar list []int = []int{1}\nowner.Choose[[]int](list)\n", "BASHPP-EGENERIC-CONSTRAINT: []int does not satisfy constraint for A in Choose\n"},
		{"method_expression_constraint", "type Vessel int\nfunc (v Vessel) Choose[A comparable](a A) A { return a; }\nvar owner Vessel = 1\nvar list []int = []int{1}\nVessel.Choose(owner, list)\n", "BASHPP-EGENERIC-CONSTRAINT: []int does not satisfy constraint for A in Choose\n"},
		{"renamed_interface", "type Vessel int\nfunc (v Vessel) Choose[A any](a A) A { return a; }\ntype Required interface { Choose(int) int }\nvar owner Vessel = 1\nvar target Required = owner\n", "BASHPP-EINTERFACE-GENERIC: Vessel method Choose declares type parameters and cannot implement an interface method\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			file := parseProfile(t, tc.source, "methods.bpp")
			list := lower.CheckProfile(file, "methods.bpp")
			qt.Assert(t, qt.Equals(renderProfile(list), tc.want))
			qt.Assert(t, qt.Equals(len(list), 1))
			qt.Assert(t, qt.IsTrue(list[0].Pos.IsValid()))
			got, status := runInterpreter(t, tc.source, "methods.bpp")
			qt.Assert(t, qt.Equals(got, tc.want))
			qt.Assert(t, qt.Equals(status, 2))
		})
	}
}

func TestCheckProfileGenericMethodControls(t *testing.T) {
	cases := []string{
		"type Vessel int\nfunc (v Vessel) Choose[A comparable](a A) A { return a; }\nvar owner Vessel = 1\nx := Vessel.Choose(owner, 3)\n",
		"type Vessel int\nfunc (v Vessel) Choose[A any](a A) A { return a; }\ntype Alias = Vessel\nvar owner Alias = 1\nx := owner.Choose[int](3)\n",
		"type Vessel int\nfunc (v Vessel) Choose(a int) int { return a; }\nvar owner Vessel = 1\nf := owner.Choose\n",
		"type Vessel int\nfunc (v Vessel) Extra[A any](a A) A { return a; }\nfunc (v Vessel) Choose(a int) int { return a; }\ntype Required interface { Choose(int) int }\nvar owner Vessel = 1\nvar target Required = owner\n",
		"type Vessel int\nfunc (v Vessel) Choose[A any](a A) A { return a; }\ntype Other int\nfunc (v Other) Choose(a int) int { return a; }\nvar owner Other = 1\nf := owner.Choose\n",
	}
	for i, source := range cases {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			qt.Assert(t, qt.IsNil(lower.CheckProfile(parseProfile(t, source, "methods.bpp"), "methods.bpp")))
			got, status := runInterpreter(t, source, "methods.bpp")
			qt.Assert(t, qt.Equals(got, ""))
			qt.Assert(t, qt.Equals(status, 0))
		})
	}
}

func TestCheckProfileEmbeddedPredeclaredInterfaces(t *testing.T) {
	for _, source := range []string{
		"type Failure string\nfunc (f Failure) Error() string { return f; }\ntype RichError interface { error }\nvar failure Failure = \"broken\"\nvar target RichError = failure\n",
		"type Failure string\nfunc (f Failure) Error() string { return f; }\ntype Alias = error\ntype Required interface { Alias }\ntype Renamed interface { Required }\nvar failure Failure = \"broken\"\nvar target Renamed = failure\n",
	} {
		file := parseProfile(t, source, "embedding.bpp")
		qt.Assert(t, qt.IsNil(lower.CheckProfile(file, "embedding.bpp")))
		got, status := runInterpreter(t, source, "embedding.bpp")
		qt.Assert(t, qt.Equals(got, ""))
		qt.Assert(t, qt.Equals(status, 0))
	}
	// Session facts can carry only the name of an earlier declaration.
	// Its unresolved embedding is not evidence of a concrete type term.
	file := parseProfile(t, "type Required interface { Earlier }\nvar target Required\n", "embedding.bpp")
	qt.Assert(t, qt.IsNil(lower.CheckProfileWithFacts(file, "embedding.bpp", &lower.ProfileFacts{Types: map[string]syntax.BashPPTypeExpr{"Earlier": nil}})))
}
