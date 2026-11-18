//go:build full

package interp_test

import "testing"

// Sprint: #319; Story: #1084; Story-ID: 7decf01bdd39
//
// A mapped package's package-level names are flattened into the interpreter's
// namespace. Keep same-package references between unexported generic types in
// that namespace too: the dependency helper must declare the exact names its
// generated generic bodies and aliases reference. This is the reduced shape of
// cmd/compile/internal/ssa's genericSparseMap and sparseEntry declarations.
func TestGoSourceS319MappedSamePackageUnexportedGenerics(t *testing.T) {
	out, stderr, err := runGoSourcePackages(t, `package main
import "test/p"
func main() { p.Run() }
`, map[string]string{"a.go": `package p

import "fmt"

type sparseKey interface { ~int | ~int32 }

type sparseEntry[K sparseKey, V any] struct {
	key K
	val V
}

type genericSparseMap[K sparseKey, V any] struct {
	dense []sparseEntry[K, V]
	sparse []int32
}

type sparseMap = genericSparseMap[int, int32]

func entries() []sparseEntry[int, int32] { return nil }

func Run() {
	var m sparseMap
	fmt.Println(len(m.dense), len(m.sparse), len(entries()))
}
`})
	if err != nil || stderr != "" || out != "0 0 0\n" {
		t.Fatalf("err=%v stdout=%q stderr=%q", err, out, stderr)
	}
}
