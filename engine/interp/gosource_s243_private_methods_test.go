//go:build full

package interp_test

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
)

// Sprint: #243; Story: #675; Story-ID: 6ee00d8029b3
func TestS243Issue24693MappedPrivateMethodIdentity(t *testing.T) {
	dir := filepath.Join(runtime.GOROOT(), "test", "fixedbugs", "issue24693.dir")
	a := exactGo1271Source(t, dir, "a.go", "9c332054db7ad7264cb5079996b0f8c2a4750639ffda807ef8c971c73c168a57")
	b := exactGo1271Source(t, dir, "b.go", "a412c17f8c8d7df642572d947a21c673bf0ba88f3661ebfb611c78b67cadf7f1")
	main := exactGo1271Source(t, dir, "c.go", "993df05f3d0750d5e59ba0a47a6ec9657a30185f4dd69b408450b71620ddd404")
	_, stderr := runGoSourcePackageSet(t, "issue24693", string(main.Data), []gosource.PackageSpec{
		{Path: "test/a", Sources: []gosource.Source{a}},
		{Path: "test/b", Sources: []gosource.Source{b}},
	})
	if stderr != "ok\nok\n" {
		t.Fatalf("private method package identity: %q", stderr)
	}
}

func TestS243Embed3MappedPrivateMethodIdentity(t *testing.T) {
	dir := filepath.Join(runtime.GOROOT(), "test", "interface", "embed3.dir")
	p := exactGo1271Source(t, dir, "embed0.go", "4a5c7c394b1f84113c156b9ab49e6ac990c4631d96a32a68892fa1cb53ca1b65")
	main := exactGo1271Source(t, dir, "embed1.go", "9cf45b629f2c46dff989d69450197f86eac130a2f927b1dff8efab0c34604a82")
	_, stderr := runGoSourceMultiPackage(t, "embed3", string(main.Data), "test/embed0", p.Name, string(p.Data))
	if stderr != "" {
		t.Fatalf("mapped private method matching: %q", stderr)
	}
}

func exactGo1271Source(t *testing.T, dir, name, digest string) gosource.Source {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != digest {
		t.Fatalf("requires exact Go1.27.1 fixture %s: sha256 %s", name, got)
	}
	return gosource.Source{Name: name, Data: data}
}

func TestS243MappedPrivateMethodPointerValueAndPackageNegative(t *testing.T) {
	p := `package p
type Value interface { hidden() }
type Pointer interface { ptr() }
type T struct{}
func (T) hidden() {}
func (*T) ptr() {}
`
	main := `package main
import "./p"
type Local struct{}
func (Local) hidden() {}
func (*Local) ptr() {}
func main() {
 var pt p.T
 var local Local
 _, pv := any(pt).(p.Value); _, ppv := any(pt).(p.Pointer); _, ppp := any(&pt).(p.Pointer)
 _, lv := any(local).(p.Value); _, lp := any(&local).(p.Pointer)
 println(pv, ppv, ppp, lv, lp)
}
`
	_, stderr := runGoSourceMultiPackage(t, "private-method-negative", main, "test/p", "p.go", p)
	if stderr != "true false true false false\n" {
		t.Fatalf("pointer/value or package-private control: %q", stderr)
	}
}
