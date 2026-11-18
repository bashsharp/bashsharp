//go:build full

package interp_test

// Sprint: #281; Story: #809; Story-ID: fac7e14af4a8
//
// A testing callback runs on a frame cloned from its registration-time
// template, so the package variables it is entitled to reach have to be shared
// into that clone. The entitlement is not limited to the callback's own
// package: cmd/compile/internal/types' TestSymCompare only ever names
// types.NewPkg, and NewPkg reads and writes the `pkgMap` global of the package
// it is declared in. Sharing same-package globals alone left that map absent
// from the frame, so the flattened `__gosource_pkg_0_pkgMap` read at the top of
// NewPkg failed with BASHPP-ESELECTOR-ROOT.

import (
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
)

// s809TypesPackage mirrors cmd/compile/internal/types' package-level pkgMap and
// the NewPkg/Lookup/CompareSyms trio TestSymCompare drives, with the compiler's
// own obj/base dependencies replaced by equivalent standard-library work.
const s809TypesPackage = `package types

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// pkgMap maps a package path to a package.
var pkgMap = make(map[string]*Pkg)

type Pkg struct {
	Path   string
	Name   string
	Prefix string
	Syms   map[string]*Sym

	Direct bool
}

// NewPkg returns a new Pkg for the given package path and name.
func NewPkg(path, name string) *Pkg {
	if p := pkgMap[path]; p != nil {
		if name != "" && p.Name != name {
			panic(fmt.Sprintf("conflicting package names %s and %s for path %q", p.Name, name, path))
		}
		return p
	}

	p := new(Pkg)
	p.Path = path
	p.Name = name
	p.Prefix = strings.ReplaceAll(path, "/", "%2f")
	p.Syms = make(map[string]*Sym)
	pkgMap[path] = p

	return p
}

func PkgMap() map[string]*Pkg { return pkgMap }

var nopkg = &Pkg{Syms: make(map[string]*Sym)}

type Sym struct {
	Pkg  *Pkg
	Name string
}

func (pkg *Pkg) Lookup(name string) *Sym {
	s, _ := pkg.LookupOK(name)
	return s
}

func (pkg *Pkg) LookupOK(name string) (s *Sym, existed bool) {
	if pkg == nil {
		pkg = nopkg
	}
	if s := pkg.Syms[name]; s != nil {
		return s, true
	}
	s = &Sym{Name: name, Pkg: pkg}
	pkg.Syms[name] = s
	return s, false
}

func IsExported(name string) bool {
	if r := name[0]; r < utf8.RuneSelf {
		return 'A' <= r && r <= 'Z'
	}
	r, _ := utf8.DecodeRuneInString(name)
	return unicode.IsUpper(r)
}

// CompareSyms returns the ordering of a and b, as for cmp.Compare.
func CompareSyms(a, b *Sym) int {
	if a == b {
		return 0
	}
	if a == nil {
		return -1
	}
	if b == nil {
		return +1
	}
	ea := IsExported(a.Name)
	eb := IsExported(b.Name)
	if ea != eb {
		if ea {
			return -1
		} else {
			return +1
		}
	}
	if r := strings.Compare(a.Name, b.Name); r != 0 {
		return r
	}
	if !ea {
		return strings.Compare(a.Pkg.Path, b.Pkg.Path)
	}
	return 0
}
`

// TestS809CrossPackageGlobalMapInTestingCallback is TestSymCompare itself: the
// interned symbols only sort into the expected order if every NewPkg and
// Lookup call reaches the one shared pkgMap.
func TestS809CrossPackageGlobalMapInTestingCallback(t *testing.T) {
	types := gosource.PackageSpec{Path: "example.com/types", Sources: []gosource.Source{
		s249Source("types.go", s809TypesPackage),
	}}
	xtest := gosource.PackageSpec{Path: "example.com/types_test", Sources: []gosource.Source{
		s249Source("sym_test.go", `package types_test

import (
	"fmt"
	"slices"
	"testing"

	"example.com/types"
)

// sameSyms stands in for the upstream reflect.DeepEqual call: interning means
// pointer identity is the property under test, and the interpreter has its own
// separate gap around slices.Equal over pointer elements.
func sameSyms(a, b []*types.Sym) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestSymCompare(t *testing.T) {
	var (
		local = types.NewPkg("", "")
		abc   = types.NewPkg("abc", "")
		uvw   = types.NewPkg("uvw", "")
		xyz   = types.NewPkg("xyz", "")
		gr    = types.NewPkg("gr", "")
	)

	data := []*types.Sym{
		abc.Lookup("b"),
		local.Lookup("B"),
		local.Lookup("C"),
		uvw.Lookup("c"),
		local.Lookup("C"),
		gr.Lookup("φ"),
		local.Lookup("Φ"),
		xyz.Lookup("b"),
		abc.Lookup("a"),
		local.Lookup("B"),
	}
	want := []*types.Sym{
		local.Lookup("B"),
		local.Lookup("B"),
		local.Lookup("C"),
		local.Lookup("C"),
		local.Lookup("Φ"),
		abc.Lookup("a"),
		abc.Lookup("b"),
		xyz.Lookup("b"),
		uvw.Lookup("c"),
		gr.Lookup("φ"),
	}
	if len(data) != len(want) {
		t.Fatal("want and data must match")
	}
	if sameSyms(data, want) {
		t.Fatal("data must be shuffled")
	}
	slices.SortFunc(data, types.CompareSyms)
	if !sameSyms(data, want) {
		t.Errorf("sorting failed")
	}
	fmt.Println("packages", len(types.PkgMap()), "local syms", len(local.Syms), abc.Prefix)
}
`),
	}}
	driver := s249Source("_testmain.go", `package main

import (
	"os"
	"testing"
	"testing/internal/testdeps"

	_ "example.com/types"
	_xtest "example.com/types_test"
)

var tests = []testing.InternalTest{{"TestSymCompare", _xtest.TestSymCompare}}
var benchmarks = []testing.InternalBenchmark{}
var fuzzTargets = []testing.InternalFuzzTarget{}
var examples = []testing.InternalExample{}

func main() {
	m := testing.MainStart(testdeps.TestDeps{}, tests, benchmarks, fuzzTargets, examples)
	os.Exit(m.Run())
}
`)

	got, err := runS249PackageTestMain(t, []gosource.Source{driver}, []gosource.PackageSpec{types, xtest})
	if err != nil {
		t.Fatalf("Runner: %v; stderr: %s", err, got.stderr)
	}
	// `go test` over the same sources prints exactly this.
	want := s249GoSourceOutcome{stdout: "packages 5 local syms 3 abc\nPASS\n"}
	if got != want {
		t.Fatalf("Runner %+v; want %+v", got, want)
	}
}

// TestS809CrossPackageGlobalsStayShared keeps the storage identity explicit:
// a dependency package's map, scalar and slice globals mutated from inside a
// testing callback — including from a nested subtest — must be the same cells
// the test main observes after M.Run, and a same-named local in the callback
// must still shadow rather than alias them.
func TestS809CrossPackageGlobalsStayShared(t *testing.T) {
	state := gosource.PackageSpec{Path: "example.com/state", Sources: []gosource.Source{
		s249Source("state.go", `package state

var counts = make(map[string]int)
var total int
var trail []string

func Record(key string) {
	counts[key]++
	total++
	trail = append(trail, key)
}

func Counts() (int, int, int) { return len(counts), counts["outer"], total }

func Trail() []string { return trail }
`),
	}}
	xtest := gosource.PackageSpec{Path: "example.com/state_test", Sources: []gosource.Source{
		s249Source("state_test.go", `package state_test

import (
	"testing"

	"example.com/state"
)

func TestRecord(t *testing.T) {
	// A local of the same source name must shadow, never alias, the global.
	total := "shadow"
	state.Record("outer")
	state.Record("outer")
	t.Run("nested", func(t *testing.T) {
		state.Record("inner")
	})
	if total != "shadow" {
		t.Fatal("local shadow lost")
	}
	if keys, outer, sum := state.Counts(); keys != 2 || outer != 2 || sum != 3 {
		t.Fatalf("inside callback: %d %d %d", keys, outer, sum)
	}
}
`),
	}}
	driver := s249Source("_testmain.go", `package main

import (
	"fmt"
	"os"
	"testing"
	"testing/internal/testdeps"

	_xtest "example.com/state_test"
	"example.com/state"
)

var tests = []testing.InternalTest{{"TestRecord", _xtest.TestRecord}}
var benchmarks = []testing.InternalBenchmark{}
var fuzzTargets = []testing.InternalFuzzTarget{}
var examples = []testing.InternalExample{}

func main() {
	m := testing.MainStart(testdeps.TestDeps{}, tests, benchmarks, fuzzTargets, examples)
	code := m.Run()
	keys, outer, sum := state.Counts()
	fmt.Println("after", keys, outer, sum, state.Trail())
	os.Exit(code)
}
`)

	got, err := runS249PackageTestMain(t, []gosource.Source{driver}, []gosource.PackageSpec{state, xtest})
	if err != nil {
		t.Fatalf("Runner: %v; stderr: %s", err, got.stderr)
	}
	want := s249GoSourceOutcome{stdout: "PASS\nafter 2 2 3 [outer outer inner]\n"}
	if got != want {
		t.Fatalf("Runner %+v; want %+v", got, want)
	}
}
