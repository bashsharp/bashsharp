// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp_test

import (
	"strings"
	"testing"
)

func TestBashPPGenericComparableNamedTypes(t *testing.T) {
	tests := []struct {
		name string
		typ  string
		init string
		want string
	}{
		{"named slice", "type Values []int", "Values{1}", "BASHPP-EGENERIC-CONSTRAINT:"},
		{"named map", "type Values map[string]int", "Values{\"a\": 1}", "BASHPP-EGENERIC-CONSTRAINT:"},
		{"struct containing slice", "type Values struct { Items []int }", "Values{Items: []int{1}}", "BASHPP-EGENERIC-CONSTRAINT:"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src := tc.typ + "\nfunc accept[T comparable](v T) { echo accepted }\nv := " + tc.init + "\naccept(v)\n"
			if got := runBashPPFunc(t, src); !strings.Contains(got, tc.want) {
				t.Fatalf("output = %q, want to contain %q", got, tc.want)
			}
		})
	}
}

func TestBashPPGenericComparableNamedTypesAccepted(t *testing.T) {
	tests := []struct {
		typ  string
		init string
	}{
		{"type Values [2]int", "Values{1, 2}"},
		{"type Values struct { Count int; Name string }", "Values{Count: 1, Name: \"n\"}"},
	}
	for _, tc := range tests {
		src := tc.typ + "\nfunc accept[T comparable](v T) { echo ok }\nfunc main() {\n v := " + tc.init + "\n accept(v)\n}\nmain()\n"
		if got := runBashPPFunc(t, src); got != "ok\n" {
			t.Fatalf("output = %q, want %q", got, "ok\\n")
		}
	}
}

func TestBashPPGenericMultipleInferenceAndNamedConstraint(t *testing.T) {
	const src = `type Stringer interface { String() string }
type Name string
func (v Name) String() string { return v }
func choose[A Stringer, B any](a A, b B) B { return b }
func main() {
 var n Name = "n"
 x := choose(n, 7)
 echo "$x"
}
main()
`
	if got := runBashPPFunc(t, src); got != "7\n" {
		t.Fatalf("output = %q, want %q", got, "7\\n")
	}
}

func TestBashPPGenericNamedTypeRuntime(t *testing.T) {
	const src = `type Box[T any] struct { Value T }
func main() {
 var b Box[int] = Box[int]{Value:7}
 echo "$b"
}
main()
`
	if got := runBashPPFunc(t, src); !strings.Contains(got, `"Value":7`) {
		t.Fatalf("output = %q, want Box value", got)
	}
}

func TestBashPPGenericNamedTypeRecursiveSubstitution(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			"nested named composite substitution",
			"type Pair[A, B any] struct { First A; Second B }\ntype Holder[T any] struct { Item Pair[string,T] }\nfunc main() {\n var h Holder[int] = Holder[int]{Item: Pair[string,int]{First:\"n\", Second:7}}\n echo \"$h\"\n}\nmain()\n",
			`"Second":7`,
		},
		{
			"recursive substitution arity rejection",
			"type Pair[A, B any] struct { First A; Second B }\ntype Holder[T any] struct { Item Pair[string] }\n",
			"BASHPP-EGENERIC-ARITY: Pair expects 2 type argument(s); got 1",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := runBashPPFunc(t, tc.src)
			if !strings.Contains(got, tc.want) {
				t.Fatalf("output = %q, want to contain %q", got, tc.want)
			}
		})
	}
}

func TestBashPPGenericTypeSetConstraints(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			"approximation accepts named underlying int",
			"type Age int\nfunc id[T ~int\\|string](v T) T {\n return v\n}\nvar a Age = 9\nx := id(a)\necho \"$x\"\n",
			"9\n",
		},
		{
			"union accepts string",
			"func id[T ~int\\|string](v T) T {\n return v\n}\nx := id[string](hi)\necho \"$x\"\n",
			"hi\n",
		},
		{
			"union rejects bool",
			"func id[T ~int\\|string](v T) T {\n return v\n}\nid[bool](true)\n",
			"BASHPP-EGENERIC-CONSTRAINT:",
		},
		{
			"named type arity",
			"type Box[T any] struct { Value T }\nvar b Box = Box[int]{Value:1}\n",
			"BASHPP-EGENERIC-ARITY: Box expects 1 type argument(s); got 0",
		},
		{
			"named type constraint",
			"type Box[T comparable] struct { Value T }\nvar xs []int = []int{1}\nvar b Box[[]int]\n",
			"BASHPP-EGENERIC-CONSTRAINT:",
		},
		{
			"recursive generic value cycle",
			"type Bad[T any] Bad[T]\n",
			"cyclic type declaration: Bad",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := runBashPPFunc(t, tc.src)
			if !strings.Contains(got, tc.want) {
				t.Fatalf("output = %q, want to contain %q", got, tc.want)
			}
		})
	}
}

func TestBashPPGenericNamedTypeMethodSets(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			"value method on instantiated named type",
			"type Box[T any] struct { Value T }\nfunc (b Box[T]) Show() {\n echo show\n}\nfunc main() {\n var b Box[int] = Box[int]{Value:1}\n b.Show()\n}\nmain()\n",
			"show\n",
		},
		{
			"pointer method through addressable instantiated value",
			"type Box[T any] struct { Value T }\nfunc (b *Box[T]) Touch() {\n echo touch\n}\nfunc main() {\n var b Box[int] = Box[int]{Value:1}\n b.Touch()\n}\nmain()\n",
			"touch\n",
		},
		{
			"pointer method not in value interface method set",
			"type Toucher interface { Touch() }\ntype Box[T any] struct { Value T }\nfunc (b *Box[T]) Touch() {\n echo touch\n}\nfunc main() {\n var b Box[int] = Box[int]{Value:1}\n var t Toucher = b\n}\nmain()\n",
			"Box[int] does not implement interface (missing method Touch)",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := runBashPPFunc(t, tc.src)
			if !strings.Contains(got, tc.want) {
				t.Fatalf("output = %q, want to contain %q", got, tc.want)
			}
		})
	}
}

func TestBashPPGenericReceiverRequiresParameters(t *testing.T) {
	const src = "type Box[T any] struct { Value T }\nfunc (b Box) Show() {\n echo show\n}\n"
	if got := runBashPPFunc(t, src); !strings.Contains(got, "BASHPP-EGENERIC-RECEIVER: Box expects 1 receiver type parameter(s); got 0") {
		t.Fatalf("output = %q", got)
	}
}

func TestBashPPGenericRecursiveIndirectionAccepted(t *testing.T) {
	const src = "type List[T any] []List[T]\necho ok\n"
	if got := runBashPPFunc(t, src); got != "ok\n" {
		t.Fatalf("output = %q, want %q", got, "ok\\n")
	}
}

func TestBashPPGenericReceiverDeclaration(t *testing.T) {
	const src = `type Box[T any] struct { Value T }
func (b Box[T]) Show() {
 echo show
}
func main() {
 var b Box[int] = Box[int]{Value:1}
 b.Show()
}
main()
`
	if got := runBashPPFunc(t, src); got != "show\n" {
		t.Fatalf("output = %q, want %q", got, "show\\n")
	}
}

func TestBashPPGenericRepresentationCyclesAndZeroValues(t *testing.T) {
	accepted := []string{
		"type List[T any] []List[T]\nvar v List[int]\necho ok\n",
		"type Tree[T any] map[string]Tree[T]\nvar v Tree[int]\necho ok\n",
		"type Node[T any] *Node[T]\nvar v Node[int]\necho ok\n",
	}
	for _, src := range accepted {
		if got := runBashPPFunc(t, src); got != "ok\n" {
			t.Fatalf("accepted recursive type output = %q", got)
		}
	}
	for _, src := range []string{
		"type Direct[T any] Direct[T]\n",
		"type Array[T any] [1]Array[T]\n",
		"type Struct[T any] struct { Next Struct[T] }\n",
	} {
		if got := runBashPPFunc(t, src); !strings.Contains(got, "cyclic type declaration:") {
			t.Fatalf("invalid recursive type output = %q", got)
		}
	}
}

func TestBashPPGenericReceiverBindingAndMethodSets(t *testing.T) {
	const src = `type Box[T any] struct { Value T }
func (b Box[T]) Echo(v T) T { return v }
func (b *Box[T]) Pointer(v T) T { return v }
func main() {
 var b Box[int] = Box[int]{Value:1}
 x := b.Echo(7)
 y := b.Pointer(8)
 printf '%s:%s\n' "$x" "$y"
}
main()
`
	if got := runBashPPFunc(t, src); got != "7:8\n" {
		t.Fatalf("output = %q, want %q", got, "7:8\\n")
	}
}

func TestBashPPInstantiatedGenericMethodImplementsInterface(t *testing.T) {
	const src = `type Echoer interface { Echo(int) int }
type Box[T any] struct { Value T }
func (b Box[T]) Echo(v T) T { return v }
func main() {
 var b Box[int] = Box[int]{Value:1}
 var e Echoer = b
 x := e.Echo(9)
 echo "$x"
}
main()
`
	if got := runBashPPFunc(t, src); got != "9\n" {
		t.Fatalf("output = %q, want %q", got, "9\\n")
	}
}

func TestBashPPGenericNamedTypeSetConstraint(t *testing.T) {
	const src = `type Integer interface { ~int }
type Age int
func id[T Integer](v T) T {
 return v
}
var age Age = 9
x := id(age)
echo "$x"
`
	if got := runBashPPFunc(t, src); got != "9\n" {
		t.Fatalf("output = %q, want %q", got, "9\\n")
	}
}

func TestBashPPGenericNamedTypeDependentTypeSetConstraint(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			"reject outside substituted type set",
			"type Bucket[S ~[]E, E comparable] struct { Values S }\nvar bucket Bucket[[]int,string]\n",
			"BASHPP-EGENERIC-CONSTRAINT: []int does not satisfy constraint for S in Bucket",
		},
		{
			"accept substituted slice approximation",
			"type Bucket[S ~[]E, E comparable] struct { Values S }\nvar bucket Bucket[[]string,string]\necho accepted\n",
			"accepted\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := runBashPPFunc(t, tc.src)
			if !strings.Contains(got, tc.want) {
				t.Fatalf("output = %q, want to contain %q", got, tc.want)
			}
		})
	}
}

func TestBashPPGenericTypeSetInterfaceIsNotValueType(t *testing.T) {
	const src = "type Integer interface { ~int }\nvar value Integer\n"
	if got := runBashPPFunc(t, src); !strings.Contains(got, "BASHPP-EINTERFACE-TYPESET:") {
		t.Fatalf("output = %q", got)
	}
}

func TestBashPPGenericDuplicateTypeParameters(t *testing.T) {
	for _, src := range []string{
		"func f[T any, T any](v T) T {\n return v\n}\n",
		"type Box[T any, T any] struct { Value T }\n",
	} {
		if got := runBashPPFunc(t, src); !strings.Contains(got, "BASHPP-EGENERIC-PARAM: type parameter T redeclared") {
			t.Fatalf("output = %q", got)
		}
	}
}
