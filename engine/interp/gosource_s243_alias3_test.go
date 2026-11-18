//go:build full

package interp_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
)

func TestS243UnchangedAlias3(t *testing.T) {
	dir := filepath.Join(runtime.GOROOT(), "test", "alias3.dir")
	read := func(name string) gosource.Source {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return gosource.Source{Name: path, Data: data}
	}
	packages := []gosource.PackageSpec{
		{Path: "test/a", Sources: []gosource.Source{read("a.go")}},
		{Path: "test/b", Sources: []gosource.Source{read("b.go")}},
	}
	if got := runS243MappedReflection(t, read("c.go"), packages, "test"); got != "" {
		t.Fatalf("output %q", got)
	}
}

func TestS243EmbeddedAliasDeclarationIdentity(t *testing.T) {
	src := func(name, text string) gosource.Source { return gosource.Source{Name: name, Data: []byte(text)} }
	packages := []gosource.PackageSpec{
		{Path: "test/a", Sources: []gosource.Source{src("a.go", `package a
type Alias2 = Alias
type Alias = Value
type Value int
type S struct {Value; Alias; Alias2}`)}},
		{Path: "test/b", Sources: []gosource.Source{src("b.go", `package b
type Value string
type Alias = Value
type S struct {Alias}`)}},
	}
	main := src("main.go", `package main
import ("fmt"; "./a"; "./b")
type Base int
type RootAlias = Base
func main(){
 var x a.S; x.Value=1;x.Alias=2;x.Alias2=3
 var y b.S;y.Alias="other"
 type Alias = string
 type S struct{Alias}
 z:=S{"local"}
 fmt.Println(x.Value,x.Alias,x.Alias2,y.Alias,z.Alias)
 aliasValue:=x.Alias2
 var v any = aliasValue
 _,aok:=v.(a.Value);_,bok:=v.(b.Value)
 fmt.Println(aok,bok)
 type First = struct{N int}
 type Second = struct{N string}
 var first First
 var boxed any = first
 _,same:=boxed.(Second)
 fmt.Println(same)
 var root RootAlias
 var rootBox any = root
 {
  type Base = string
  _,wrong:=rootBox.(Base)
  _,right:=rootBox.(RootAlias)
  fmt.Println(wrong,right)
 }
}`)
	if got := runS243MappedReflection(t, main, packages, "test"); got != "1 2 3 other local\ntrue false\nfalse\nfalse true\n" {
		t.Fatalf("output %q", got)
	}
	for _, source := range []string{
		`package bad;type A = B;type B = A`,
		`package bad;type A = int;type A = string`,
		`package bad;func f(){type S struct{Later};type Later = int;_ = S{}}`,
	} {
		_, err := gosource.Load([]gosource.Source{src("main.go", `package main;import _ "./bad";func main(){}`)}, gosource.Options{RunMain: true, ImportBase: "test", Packages: []gosource.PackageSpec{{Path: "test/bad", Sources: []gosource.Source{src("bad.go", source)}}}})
		if err == nil {
			t.Fatalf("accepted invalid alias declaration %s", source)
		}
		if !strings.Contains(err.Error(), "bad.go") {
			t.Fatalf("wrong diagnostic %v", err)
		}
	}
}
