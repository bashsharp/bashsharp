//go:build full

package interp_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func runS243MappedReflection(t *testing.T, source gosource.Source, packages []gosource.PackageSpec, base string) string {
	t.Helper()
	program, err := gosource.Load([]gosource.Source{source}, gosource.Options{RunMain: true, ImportBase: base, Packages: packages})
	if err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(t.TempDir()), interp.StdIO(nil, &out, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := runner.Run(ctx, program.File); err != nil {
		t.Fatalf("run: %v stdout=%q stderr=%q", err, out.String(), stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr: %s", stderr.String())
	}
	return out.String()
}

func TestS243MappedReflectIdentity(t *testing.T) {
	src := func(name, text string) gosource.Source { return gosource.Source{Name: name, Data: []byte(text)} }
	packages := []gosource.PackageSpec{
		{Path: "test/one", Sources: []gosource.Source{src("one.go", "package same; type T int; type Alias = T")}},
		{Path: "test/two", Sources: []gosource.Source{src("two.go", "package same; type T int")}},
	}
	main := src("main.go", `package main
import ("fmt"; "reflect"; a "./one"; b "./two"; "time"; "sort")
type T int
func main(){
 x,y:=reflect.TypeOf(a.T(0)),reflect.TypeOf(b.T(0))
 fmt.Println(x.PkgPath(), x.Name(), x.String())
 fmt.Println(y.PkgPath(), y.Name(), y.String())
 fmt.Println(x==y,x==reflect.TypeOf(a.Alias(0)),x.AssignableTo(y),x.ConvertibleTo(y))
 path:=x.PkgPath; name:=x.Name
 fmt.Println(path(),name())
 fmt.Println(reflect.TypeOf((*a.T)(nil)).PkgPath(),reflect.TypeOf((*a.T)(nil)).Name(),reflect.TypeOf((*a.T)(nil)).String())
 fmt.Println(reflect.TypeOf([]a.T{}).String())
 fmt.Println(reflect.TypeOf(T(0)).PkgPath(),reflect.TypeOf(T(0)).Name())
 fmt.Println(reflect.TypeOf(time.Duration(0)).PkgPath(),reflect.TypeOf(time.Duration(0)).Name())
 var message sort.IntSlice
 fmt.Println(reflect.TypeOf(message).String())
 plain:=reflect.ChanOf(reflect.BothDir,reflect.ChanOf(reflect.RecvDir,reflect.TypeOf(int(0))))
 mapped:=reflect.ChanOf(reflect.BothDir,reflect.ChanOf(reflect.RecvDir,x))
 fmt.Println(plain.String(),mapped.String())
}`)
	want := "test/one T same.T\ntest/two T same.T\nfalse true false true\ntest/one T\n  *same.T\n[]same.T\nmain T\ntime Duration\nsort.IntSlice\nchan (<-chan int) chan (<-chan same.T)\n"
	if got := runS243MappedReflection(t, main, packages, "test"); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestS243UnchangedBug248Reflection(t *testing.T) {
	dir := filepath.Join(runtime.GOROOT(), "test", "fixedbugs", "bug248.dir")
	read := func(name string) gosource.Source {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return gosource.Source{Name: path, Data: data}
	}
	packages := []gosource.PackageSpec{
		{Path: "test/bug0", Sources: []gosource.Source{read("bug0.go")}},
		{Path: "test/bug1", Sources: []gosource.Source{read("bug1.go")}},
	}
	if got := runS243MappedReflection(t, read("bug3.go"), packages, "test"); got != "" {
		t.Fatalf("output %q", got)
	}
}
