//go:build full

package interp_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Sprint: #243; Story: #672; Story-ID: fa5b3cf5a929

func TestS243OriginalIssue49547GenericTypeFormatting(t *testing.T) {
	source, err := os.ReadFile(filepath.Join(runtime.GOROOT(), "test", "typeparam", "issue49547.go"))
	if err != nil {
		t.Fatal(err)
	}
	typedSendThreeModes(t, string(source))
}

// Two local generic declarations with the same source name are distinct
// dynamic types. Their public reflection spelling remains the declared name.
func TestS243LocalGenericInterfaceIdentity(t *testing.T) {
	differGoSource(t, `package main
import (
 "fmt"
 "reflect"
)
func one() any { type T[_ any] int; return T[int](0) }
func two() any { type T[_ any] int; return T[int](0) }
func main() {
 p, q := one(), two()
 fmt.Println(p == q)
 fmt.Println(reflect.TypeOf(p).String(), reflect.TypeOf(q).String())
}
`, nil, "")
}

// A package declaration and a same-spelled local declaration remain distinct;
// both retain the public Go spelling rather than a registry-private name.
func TestS243PackageAndLocalGenericInterfaceIdentity(t *testing.T) {
	differGoSource(t, `package main
import (
 "fmt"
 "reflect"
)
type T[_ any] int
func packageValue() any { return T[int](0) }
func localValue() any { type T[_ any] int; return T[int](0) }
func main() {
 p, q := packageValue(), localValue()
 fmt.Println(p == q)
 fmt.Println(reflect.TypeOf(p).String(), reflect.TypeOf(q).String())
}
`, nil, "")
}

func TestS243OriginalIssue54456GenericIdentity(t *testing.T) {
	source, err := os.ReadFile(filepath.Join(runtime.GOROOT(), "test", "typeparam", "issue54456.go"))
	if err != nil {
		t.Fatal(err)
	}
	typedSendThreeModes(t, string(source))
}

// This is the unchanged Go 1.27.1 runindir root. Its imported generic alias
// has no declared named identity: the bridge must register the concrete
// structural instantiation under the imported package's spelling, rather than
// treating the helper's generated local name as a public type identity.
func TestS243UnchangedIssue68526(t *testing.T) {
	dir := filepath.Join(runtime.GOROOT(), "test", "fixedbugs", "issue68526.dir")
	read := func(name string) gosource.Source {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return gosource.Source{Name: path, Data: data}
	}
	program, err := gosource.Load([]gosource.Source{read("main.go")}, gosource.Options{
		RunMain: true, ImportBase: "issue68526.dir",
		Packages: []gosource.PackageSpec{{Path: "issue68526.dir/a", Sources: []gosource.Source{read("a/a.go")}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var out, errout bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &out, &errout))
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), program.File); err != nil {
		t.Fatalf("interpreted unchanged root: %v; stdout=%q stderr=%q", err, out.String(), errout.String())
	}
	if out.Len() != 0 || errout.Len() != 0 {
		t.Fatalf("interpreted unchanged root output: stdout=%q stderr=%q", out.String(), errout.String())
	}
}

// A generic alias is structural even when two linked packages spell it with
// the same declaration name. A defined generic type is not: its package
// identity must remain part of the dynamic type. These controls prevent the
// alias registry entry from becoming a package-name fallback.
func TestS243ImportedGenericAliasAndPackageIdentityControls(t *testing.T) {
	dep := func(name string) gosource.PackageSpec {
		return gosource.PackageSpec{Path: "test/" + name, Sources: []gosource.Source{{Name: name + ".go", Data: []byte(`package ` + name + `
type Named[T any] struct{ F T }
type Alias[T any] = struct{ F T }
`)}}}
	}
	out, stderr := runGoSourcePackageSet(t, "s243genericaliases", `package main
import ("fmt"; "./a"; "./b")
func main() {
 var an any = a.Named[string]{}
 var bn any = b.Named[string]{}
 var aa any = a.Alias[string]{}
 var ba any = b.Alias[string]{}
 fmt.Println(an == bn, aa == ba, aa == any(struct{ F string }{}))
}`, []gosource.PackageSpec{dep("a"), dep("b")})
	if stderr != "" || out != "false true true\n" {
		t.Fatalf("stdout=%q stderr=%q", out, stderr)
	}
}
