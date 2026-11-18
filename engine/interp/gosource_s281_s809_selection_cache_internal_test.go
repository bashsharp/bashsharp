//go:build full

// Sprint: #281; Story: #809; Story-ID: fac7e14af4a8

package interp

import (
	"bytes"
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// s809Run runs a Go-source program with REPS replaced and hands back the runner,
// so a test can measure the per-runner tables the run left behind. It insists on
// the program's own output: a table measured after a loop that did not run says
// nothing.
func s809Run(t *testing.T, source string, reps int, want string) *Runner {
	t.Helper()
	source = strings.ReplaceAll(source, "REPS", strconv.Itoa(reps))
	var stdout, stderr bytes.Buffer
	r, err := New(Lang(syntax.LangBashPP), Dir(t.TempDir()), StdIO(nil, &stdout, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	p, err := gosource.Parse(strings.NewReader(source), filepath.Join(r.Dir, "original.go"), gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), p.File); err != nil {
		t.Fatalf("run: %v; stderr=%q", err, stderr.String())
	}
	if got := stdout.String() + stderr.String(); got != want {
		t.Fatalf("output %q, want %q", got, want)
	}
	return r
}

// The selector cache is a memo of a pure function of the static type tables, so
// its size is a property of the PROGRAM — the distinct (type, selector) pairs it
// spells — and not of how many times the program runs them. It used to be keyed
// by the type node handed to resolution; a root reached through a value (a
// pointer cell's `*T`, an interface box's dynamic type) is synthesized per
// evaluation, so the same static type was re-keyed under a fresh pointer on
// every access and the map grew for the life of the runner: 201 entries at 100
// iterations, 20001 at 10000, roughly 540 bytes an iteration that was never
// released.
func TestS809SelectionCacheBoundedByTypesNotByAccesses(t *testing.T) {
	// Each program prints the loop's accumulated total, so the count of
	// iterations the measurement is taken over is checked rather than assumed.
	sources := map[string]struct{ source, small, big string }{
		"type_switch": {source: `package main

type Expr interface{ isExpr() }

type Name struct{ Value string }

func (n *Name) isExpr() {}

func box(v string) Expr { return &Name{Value: v} }

func main() {
	sum := 0
	for r := 0; r < REPS; r++ {
		e := box("p")
		switch v := e.(type) {
		case *Name:
			sum += len(v.Value)
		}
	}
	println(sum)
}
`, small: "10\n", big: "1000\n"},
		"interface_box": {source: `package main

type Expr interface{ isExpr() }

type Name struct{ Value string }

func (n *Name) isExpr() {}

func nilExpr(has bool) Expr {
	if !has {
		return nil
	}
	return &Name{Value: "T"}
}

func main() {
	sum := 0
	for r := 0; r < REPS; r++ {
		var e Expr = nilExpr(r%2 == 0)
		if e == nil {
			sum++
		}
	}
	println(sum)
}
`, small: "5\n", big: "500\n"},
		"embedded_field_and_method": {source: `package main

type base struct{ count int }

func (b *base) bump() { b.count++ }

type holder struct {
	base
	name string
}

func main() {
	h := &holder{name: "h"}
	for r := 0; r < REPS; r++ {
		h.bump()
		h.count += len(h.name)
	}
	println(h.count)
}
`, small: "20\n", big: "2000\n"},
	}
	for label, program := range sources {
		t.Run(label, func(t *testing.T) {
			small := len(s809Run(t, program.source, 10, program.small).bashPPSelectionCache)
			big := len(s809Run(t, program.source, 1000, program.big).bashPPSelectionCache)
			if small != big {
				t.Fatalf("selection cache grew with iterations: 10 reps=%d entries, 1000 reps=%d entries", small, big)
			}
			// A handful of (type, selector) pairs is all these programs spell;
			// the bound is the point, so keep it loose but meaningful.
			if big > 32 {
				t.Fatalf("selection cache holds %d entries for a program with a few selectors", big)
			}
		})
	}
}

// Two functions each declaring `type s struct{…}` declare distinct types, and
// the canonical key carries the declaration's lexical scope so one's fields are
// never resolved for the other. A shadowing declaration also drops the memo, so
// an entry cannot outlive the declaration it resolved against.
func TestS809SelectionRootKeyKeepsLocalTypesDistinct(t *testing.T) {
	source := `package main

func first() int {
	type s struct{ a int }
	v := s{a: 3}
	return v.a
}

func second() int {
	type s struct{ b int }
	v := s{b: 4}
	return v.b
}

func main() {
	total := 0
	for i := 0; i < 20; i++ {
		total += first() + second()
	}
	println(total)
}
`
	r := s809Run(t, source, 1, "140\n")
	if n := len(r.bashPPSelectionCache); n > 32 {
		t.Fatalf("selection cache holds %d entries across repeated local declarations", n)
	}
}

// The canonical key must refuse a root whose spelling is not its identity: an
// anonymous struct or interface literal (two distinct literals spell alike), a
// type parameter, and a named type the executing frame binds to a type argument.
func TestS809SelectionRootKeyRefusesUnstableRoots(t *testing.T) {
	r, err := New(Lang(syntax.LangBashPP), Dir(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	named := func(name string) *syntax.BashPPNamedType {
		return &syntax.BashPPNamedType{Name: &syntax.Lit{Value: name}}
	}
	if key, ok := r.bashPPSelectionRootKey(named("T")); !ok || key != "T" {
		t.Fatalf("named root key = %q, %v; want \"T\", true", key, ok)
	}
	pointer := &syntax.BashPPPointerType{Element: named("T")}
	if key, ok := r.bashPPSelectionRootKey(pointer); !ok || key != "*T" {
		t.Fatalf("pointer root key = %q, %v; want \"*T\", true", key, ok)
	}
	// The same spelling from a second, freshly allocated node is the same key:
	// this is what bounds the cache.
	again := &syntax.BashPPPointerType{Element: named("T")}
	first, _ := r.bashPPSelectionRootKey(pointer)
	second, _ := r.bashPPSelectionRootKey(again)
	if first != second {
		t.Fatalf("equal types keyed differently: %q vs %q", first, second)
	}
	generic := named("Box")
	generic.TypeArgs = []*syntax.BashPPTypeArg{{ArgType: named("int")}}
	if key, ok := r.bashPPSelectionRootKey(generic); !ok || key != "Box[int]" {
		t.Fatalf("generic root key = %q, %v; want \"Box[int]\", true", key, ok)
	}
	for label, typ := range map[string]syntax.BashPPTypeExpr{
		"nil":        nil,
		"struct":     &syntax.BashPPStructType{},
		"interface":  &syntax.BashPPInterfaceType{},
		"func":       &syntax.BashPPFuncType{},
		"slice":      &syntax.BashPPCollectionType{Kind: "slice", Element: named("int")},
		"type param": &syntax.BashPPTypeParamType{Name: &syntax.Lit{Value: "T"}},
	} {
		if key, ok := r.bashPPSelectionRootKey(typ); ok {
			t.Errorf("%s root keyed as %q; want refused", label, key)
		}
	}
	// A name the frame binds to a type argument means the frame's type, not the
	// program's, so it cannot be keyed by its spelling either.
	r.bashPPTypeParamArgs = map[string]syntax.BashPPTypeExpr{"T": named("int")}
	if key, ok := r.bashPPSelectionRootKey(named("T")); ok {
		t.Errorf("bound type parameter keyed as %q; want refused", key)
	}
	if key, ok := r.bashPPSelectionRootKey(&syntax.BashPPPointerType{Element: named("T")}); ok {
		t.Errorf("pointer to bound type parameter keyed as %q; want refused", key)
	}
}
