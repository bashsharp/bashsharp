package interp_test

// Sprint: #270; Story: #756; Story-ID: a0503101d15a

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// runGoSourcePackages runs main against one mapped package made of several
// files, the shape the go test backend hands a package root.
func runGoSourcePackages(t *testing.T, mainSource string, files map[string]string) (string, string, error) {
	t.Helper()
	var sources []gosource.Source
	for _, name := range []string{"a.go", "b.go", "c.go"} {
		if data, ok := files[name]; ok {
			sources = append(sources, gosource.Source{Name: name, Data: []byte(data)})
		}
	}
	program, err := gosource.Load([]gosource.Source{{Name: "main.go", Data: []byte(mainSource)}}, gosource.Options{
		RunMain:    true,
		ImportBase: "test",
		ImportPath: "test/main",
		Packages:   []gosource.PackageSpec{{Path: "test/p", Sources: sources}},
	})
	if err != nil {
		t.Fatalf("gosource.Load: %v", err)
	}
	var out, errout bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(t.TempDir()), interp.StdIO(nil, &out, &errout))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	err = runner.Run(ctx, program.File)
	return out.String(), errout.String(), err
}

func runS270G1Source(t *testing.T, name, source string) (string, string, error) {
	t.Helper()
	program, err := gosource.Parse(strings.NewReader(source), name+".go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatalf("gosource rejected the source: %v", err)
	}
	var out, errout bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(t.TempDir()), interp.StdIO(nil, &out, &errout))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	err = runner.Run(ctx, program.File)
	return out.String(), errout.String(), err
}

// Each file of a mapped package binds its own alias for the same dependency
// import; a map, pointer or inferred array of a dependency type spelled in one
// file is the same Go type in another (cmd/compile/internal/importer,
// cmd/compile/internal/types2).
func TestGoSourceS270ImportAliasTypeIdentity(t *testing.T) {
	files := map[string]string{
		"a.go": `package p
import (
	"go/token"
	"strings"
)
func Fill(m map[string]*strings.Builder, name string) *strings.Builder {
	b := &strings.Builder{}
	b.WriteString(name)
	m[name] = b
	return b
}
var toks = [...]token.Token{token.ADD, token.SUB}
`,
		"b.go": `package p
import (
	"fmt"
	"go/token"
	"strings"
)
func Run() {
	m := make(map[string]*strings.Builder)
	b := Fill(m, "x")
	var t [2]token.Token = toks
	arr := [...]token.Token{token.MUL, token.QUO}
	var u [2]token.Token = arr
	fmt.Println(b.String(), len(m), len(t), len(u))
}
`,
	}
	out, stderr, err := runGoSourcePackages(t, `package main
import "test/p"
func main() { p.Run() }
`, files)
	if err != nil || out != "x 1 2 2\n" {
		t.Fatalf("err=%v stdout=%q stderr=%q", err, out, stderr)
	}
}

// A method selected through a pointer field (r.p.mk()) binds that pointer as
// its receiver: the body may store or return it where *T is required.
func TestGoSourceS270PointerFieldReceiver(t *testing.T) {
	out, stderr, err := runS270G1Source(t, "s270-pointer-field-receiver", `package main
import "fmt"
type T struct{ n int }
type R struct{ p *T }
type E struct{ *T }
func (t *T) mk() *R { return &R{p: t} }
func (t *T) direct() int { x := t.mk(); return x.p.n }
func main() {
	t := &T{n: 1}
	r := &R{p: t}
	fmt.Println(r.p.mk().p == t, r.p.direct())
	e := E{t}
	fmt.Println(e.mk().p == t)
}`)
	if err != nil || out != "true 1\ntrue\n" {
		t.Fatalf("err=%v stdout=%q stderr=%q", err, out, stderr)
	}
}

// Go admits methods declared through an alias of a defined type; they belong
// to the defined type, as do implicitly typed constants of such an alias
// (cmd/compile/internal/syntax: type token = Token).
func TestGoSourceS270AliasReceiver(t *testing.T) {
	out, stderr, err := runS270G1Source(t, "s270-alias-receiver", `package main
import "fmt"
type Tkn uint
type tkn = Tkn
const (
	_ tkn = iota
	A
	B
)
func (t tkn) Str() string { return fmt.Sprint("tok", uint(t)) }
func main() {
	var x Tkn = 2
	fmt.Println(x.Str())
	for tok := A; tok <= B; tok++ {
		fmt.Println(tok.Str())
	}
}`)
	if err != nil || out != "tok2\ntok1\ntok2\n" {
		t.Fatalf("err=%v stdout=%q stderr=%q", err, out, stderr)
	}
}

// An array length naming a dependency constant has the checker's value
// (cmd/compile/internal/typecheck: var okfor [ir.OEND][]bool).
func TestGoSourceS270ImportedConstArrayLength(t *testing.T) {
	out, stderr, err := runS270G1Source(t, "s270-imported-const-length", `package main
import (
	"fmt"
	"reflect"
	"unicode/utf8"
)
var a [utf8.UTFMax]int
var okfor [reflect.UnsafePointer][]bool
func main() {
	okfor[reflect.Int] = []bool{true}
	fmt.Println(len(a), len(okfor), okfor[reflect.Int])
}`)
	if err != nil || out != "4 26 [true]\n" {
		t.Fatalf("err=%v stdout=%q stderr=%q", err, out, stderr)
	}
}

// A field selected through an asserted pointer is addressable
// (cmd/compile/internal/types: t.extra.(*Tuple).first = t1).
func TestGoSourceS270AssertedPointerFieldAssign(t *testing.T) {
	out, stderr, err := runS270G1Source(t, "s270-asserted-pointer-field", `package main
import "fmt"
type Results struct{ Types []int }
type Tuple struct{ first, second int }
type Type struct{ extra any }
func main() {
	t := &Type{extra: new(Results)}
	t.extra.(*Results).Types = []int{1, 2}
	u := &Type{extra: &Tuple{}}
	u.extra.(*Tuple).first = 3
	(u.extra.(*Tuple)).second += 4
	fmt.Println(t.extra.(*Results).Types, *u.extra.(*Tuple))
}`)
	if err != nil || out != "[1 2] {3 4}\n" {
		t.Fatalf("err=%v stdout=%q stderr=%q", err, out, stderr)
	}
}

// A call stored in an interface element is converted to the interface, both
// when it returns the interface and when it returns a concrete pointer
// (cmd/compile/internal/syntax: list := []Expr{x, p.expr()}).
func TestGoSourceS270InterfaceElementCall(t *testing.T) {
	out, stderr, err := runS270G1Source(t, "s270-interface-element-call", `package main
import "fmt"
type Expr interface{ Pos() int }
type Name struct{ p int }
func (n *Name) Pos() int { return n.p }
func mk() *Name { return &Name{7} }
func bin(x Expr) Expr {
	if x == nil {
		x = mk()
	}
	return x
}
func main() {
	var e Expr = mk()
	l := []Expr{bin(nil), e, mk()}
	fmt.Println(len(l), l[0].Pos(), l[2].Pos())
}`)
	if err != nil || out != "3 7 7\n" {
		t.Fatalf("err=%v stdout=%q stderr=%q", err, out, stderr)
	}
}
