//go:build full

package interp_test

import (
	"strings"
	"testing"
)

// Sprint: #243; Story: #674; Story-ID: 63073886bfce
//
// Native bridge type registration by lexical identity. Each positive
// control runs unchanged through native Go and through the Runner; the
// native outcome is the expectation.

// gcc65755 shape: the same local type name declared in two methods, each
// crossing to reflect under its own identity and reporting its own field.
func TestS243ScopedLocalTypeInTwoMethods(t *testing.T) {
	differGoSource(t, `package main

import (
	"fmt"
	"reflect"
)

type S1 struct{}

func (S1) Fix() string {
	type s struct {
		f int
	}
	return reflect.TypeOf(s{}).Field(0).Name
}

type S2 struct{}

func (S2) Fix() string {
	type s struct {
		g bool
	}
	return reflect.TypeOf(s{}).Field(0).Name
}

func main() {
	f1 := S1{}.Fix()
	f2 := S2{}.Fix()
	if f1 != "f" || f2 != "g" {
		panic(f1 + f2)
	}
	fmt.Println(f1, f2)
}
`, nil, "")
}

// A package-level name shadowed by a function-local declaration: a
// reference before the local declaration keeps the package type, one
// after it resolves to the local declaration, and a type composed from
// the scoped one (`[]Rec`, `*Rec`) still resolves in the helper.
func TestS243ScopedLocalTypeShadowsPackageType(t *testing.T) {
	differGoSource(t, `package main

import (
	"fmt"
	"reflect"
)

type Rec struct{ A int }

func outer() string { return reflect.TypeOf(Rec{}).Field(0).Name }

func main() {
	fmt.Println(outer(), reflect.TypeOf(Rec{}).Field(0).Name)
	type Rec struct{ B string }
	fmt.Println(reflect.TypeOf(Rec{}).Field(0).Name)
	xs := []Rec{{"a"}, {"b"}}
	fmt.Println(reflect.TypeOf(xs).Elem().Field(0).Name, len(xs))
	p := new(Rec)
	fmt.Println(reflect.TypeOf(p).Elem().NumField())
}
`, nil, "")
}

// typeparam/nested shape: the argument spellings of one instantiation used
// to leak their local references into the previously rendered descriptor,
// dropping it from the dependency-closed set. `T[int]` mentions nothing
// local and must stay registered whatever `X[GlobalInt]` mentions.
func TestS243InstantiationRefsDoNotLeak(t *testing.T) {
	differGoSource(t, `package main

import (
	"fmt"
	"reflect"
)

type symbols struct{}
type GlobalInt = symbols
type T[B any] struct{}
type X[A any] struct{ V A }

func main() {
	fmt.Println(reflect.TypeOf(T[int]{}).NumField())
	var x X[GlobalInt]
	_ = x
}
`, nil, "")
}

// typeparam/mdempsky/15 shape: a pointer to an interface literal with
// methods, reached through new(T) with T bound to the literal, is a type
// the helper materialises like an anonymous struct shape.
func TestS243AnonymousInterfacePointer(t *testing.T) {
	differGoSource(t, `package main

import (
	"fmt"
	"reflect"
)

func TypeString[T any]() string {
	return reflect.TypeOf(new(T)).Elem().String()
}

func main() {
	fmt.Println(TypeString[interface{ EGood() }]())
	fmt.Println(TypeString[interface {
		Read(p []byte) (int, error)
	}]())
	fmt.Println(reflect.TypeOf(new(interface{ M(int) string })).Elem().NumMethod())
}
`, nil, "")
}

// Negative: a scoped declaration the helper cannot materialise — a field
// whose array length is a named constant the helper never sees — is not
// registered under any identity, and a reference to it is still refused
// rather than resolved to the other declaration of the name.
func TestS243ScopedLocalTypeUnmaterialisedStillRefused(t *testing.T) {
	got := runGoSourceRunnerError(t, `package main

import "fmt"

const N = 2

func one() {
	type s struct{ A [N]int }
	fmt.Println(s{})
}

func two() {
	type s struct{ B [N]int }
	fmt.Println(s{})
}

func main() {
	one()
	two()
}
`)
	if !strings.Contains(got, "unregistered bridge type") {
		t.Fatalf("an unmaterialised scoped declaration must still be refused, got %q", got)
	}
}

// Negative: an interface literal with a type-parameter method signature
// has no fixed identity to materialise; the helper still refuses it.
func TestS243AnonymousInterfaceWithTypeParamStillRefused(t *testing.T) {
	got := runGoSourceRunnerError(t, `package main

import (
	"fmt"
	"reflect"
)

func Name[T any]() string {
	return reflect.TypeOf(new(interface{ Get() T })).Elem().String()
}

func main() {
	fmt.Println(Name[int]())
}
`)
	if !strings.Contains(got, "unregistered bridge type") {
		t.Fatalf("an unmaterialised interface shape must still be refused, got %q", got)
	}
}

// Anonymous registry keys must include the lexical identity of named types
// inside fields and method signatures, matching transported wire type names.
// The local types deliberately have identical underlying types: only their
// declarations distinguish them, so shape-only deduplication is insufficient.
func TestS243AnonymousShapesKeepScopedNamedIdentity(t *testing.T) {
	differGoSource(t, `package main
import (
 "fmt"
 "reflect"
)
func firstStruct() reflect.Type {
 type s int
 return reflect.TypeOf(struct { V s }{})
}
func secondStruct() reflect.Type {
 type s int
 return reflect.TypeOf(struct { V s }{})
}
func firstInterface() reflect.Type {
 type s int
 return reflect.TypeOf(new(interface { M(s) })).Elem()
}
func secondInterface() reflect.Type {
 type s int
 return reflect.TypeOf(new(interface { M(s) })).Elem()
}
func main() {
 a, b := firstStruct(), secondStruct()
 if a == b { panic("distinct anonymous struct identities collapsed") }
 if a != firstStruct() { panic("anonymous struct identity is unstable") }
 if a.Field(0).Type == b.Field(0).Type { panic("distinct local field identities collapsed") }
 c, d := firstInterface(), secondInterface()
 if c == d { panic("distinct anonymous interface identities collapsed") }
 if c != firstInterface() { panic("anonymous interface identity is unstable") }
 fmt.Println(a.NumField(), b.NumField(), c.NumMethod(), d.NumMethod())
}
`, nil, "")
}
