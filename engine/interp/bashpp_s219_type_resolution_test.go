//go:build full

package interp_test

// Sprint: #219; Story: #464; Story-ID: d993e87f95b7

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

func TestS219TypeResolution(t *testing.T) {
	t.Run("dot imported native alias retains checked identity", func(t *testing.T) {
		stderr := runS219TypeResolution(t, `package main
import . "go/build"
type MyContext = Context
func contextOS(c MyContext) string { return c.GOOS }
func main() {
	var c MyContext = Default
	var copy MyContext = c
	println(contextOS(copy))
}`, true)
		if stderr == "\n" || !strings.HasSuffix(stderr, "\n") {
			t.Fatalf("runtime alias value output = %q", stderr)
		}
	})

	t.Run("mapped embedded type keeps package identity", func(t *testing.T) {
		dep := `package a; type T struct{ N int }`
		main := `package main; import "./a"; type T struct{ a.T }; func main(){ var v T; v.N=7; println(v.N) }`
		_, stderr := runGoSourceMultiPackage(t, "typeidentity", main, "test/a", "a.go", dep)
		if stderr != "7\n" {
			t.Fatalf("stderr = %q", stderr)
		}
	})

	t.Run("declared comparable constraint", func(t *testing.T) {
		runS219TypeResolution(t, `package main
type C comparable
type value[T C] struct{ val T }
func equal[T C](a, b T) bool { return a == b }
func main(){ println(equal(3,3)); var v value[string]; v.val="ok"; println(v.val) }`, true)
	})

	t.Run("named term preserves identity", func(t *testing.T) {
		runS219TypeResolution(t, `package main
type MyInt int
type Box[T MyInt] struct{ val T }
func main(){ var good Box[MyInt]; good.val = 3; println(good.val) }`, true)
		runS219TypeResolution(t, `package main
type MyInt int
type Box[T MyInt] struct{ val T }
func main(){ var bad Box[int]; _ = bad }`, false)
	})

	t.Run("pointer recursion remains valid", func(t *testing.T) {
		runS219TypeResolution(t, `package main; type Node struct{ next *Node }; func main(){ var n Node; println(n.next == nil) }`, true)
	})

	t.Run("direct recursion remains invalid", func(t *testing.T) {
		runS219TypeResolution(t, `package main; type Bad struct{ next Bad }; func main(){}`, false)
	})

	t.Run("dot import collision remains rejected", func(t *testing.T) {
		_, err := gosource.Load([]gosource.Source{{Name: "main.go", Data: []byte(`package main; import (. "./a"; . "./b"); func main(){ _ = T{} }`)}}, gosource.Options{
			RunMain: true, ImportBase: "test",
			Packages: []gosource.PackageSpec{
				{Path: "test/a", Sources: []gosource.Source{{Name: "a.go", Data: []byte(`package a; type T struct{}`)}}},
				{Path: "test/b", Sources: []gosource.Source{{Name: "b.go", Data: []byte(`package b; type T struct{}`)}}},
			},
		})
		if err == nil {
			t.Fatal("colliding dot imports accepted")
		}
	})
}

func runS219TypeResolution(t *testing.T, source string, wantOK bool) string {
	t.Helper()
	program, err := gosource.Parse(strings.NewReader(source), "type_resolution.go", gosource.Options{RunMain: true})
	if err != nil {
		if wantOK {
			t.Fatal(err)
		}
		return ""
	}
	var out, errout bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(t.TempDir()), interp.StdIO(nil, &out, &errout))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err = runner.Run(ctx, program.File)
	if wantOK && err != nil {
		t.Fatalf("run: %v; stderr=%q", err, errout.String())
	}
	if !wantOK && err == nil {
		t.Fatalf("invalid recursive type accepted; output=%q", out.String())
	}
	return errout.String()
}
