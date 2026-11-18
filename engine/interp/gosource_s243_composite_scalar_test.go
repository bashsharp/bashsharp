//go:build full

package interp_test

// Sprint: #243; Story: #672; Story-ID: fa5b3cf5a929

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
)

func TestS243CompositeScalarOriginalRoots(t *testing.T) {
	t.Run("bug465", func(t *testing.T) {
		dir := filepath.Join(runtime.GOROOT(), "test", "fixedbugs", "bug465.dir")
		dep := exactGo1271Source(t, dir, "a.go", "700b0558ce514a66dc2adb654c24c758cfebfe472df2b560caeadb8c31a7effc")
		main := exactGo1271Source(t, dir, "b.go", "cf7fa8255d6350b869921b4206d4633196998723ad52154b05cad3226c579ab4")
		out, stderr := runGoSourceMultiPackage(t, "bug465", string(main.Data), "test/a", dep.Name, string(dep.Data))
		if out != "" || stderr != "" {
			t.Fatalf("unexpected output %q %q", out, stderr)
		}
	})

	t.Run("issue30908", func(t *testing.T) {
		dir := filepath.Join(runtime.GOROOT(), "test", "fixedbugs", "issue30908.dir")
		a := exactGo1271Source(t, dir, "a.go", "6eafa5ca242f2cf3d7effb5d71850b819ae07d9587a1f2bb17c7348150fb158a")
		b := exactGo1271Source(t, dir, "b.go", "c9da1e1edb629a6a9df9bafa9da17006b29028110d95c204a9b7fdd930af3b01")
		main := exactGo1271Source(t, dir, "m.go", "a42922ac9ddd471de7949903a71a1362c0c89ac2df2a7853e959687b6b017529")
		out, stderr := runGoSourcePackageSet(t, "issue30908", string(main.Data), []gosource.PackageSpec{
			{Path: "test/a", Sources: []gosource.Source{a}},
			{Path: "test/b", Sources: []gosource.Source{b}},
		})
		if out != "" || stderr != "" {
			t.Fatalf("unexpected output %q %q", out, stderr)
		}
	})
}

func TestS243CompositeScalarNegativeControls(t *testing.T) {
	typedSendThreeModes(t, `package main
type N int
type M map[int]int
func main() {
	if (M{}) == nil { panic("map literal is nil") }
	var x any = N(7)
	m := map[string]any{"x": x}
	println(m["x"] == N(7), m["x"] == int(7))
}
`)
}

func TestS243ParenthesizedCompositePointerControls(t *testing.T) {
	typedSendThreeModes(t, `package main
type T struct { A, B int }
var calls int
func next() int { calls++; return calls }
func main() {
	p := (&T{next(), next()})
	if p == nil || p.A != 1 || p.B != 2 || calls != 2 { panic("short declaration") }
	p = (&T{next(), next()})
	if p == nil || p.A != 3 || p.B != 4 || calls != 4 { panic("assignment") }
}
`)
}
