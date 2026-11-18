//go:build full

package interp

import (
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
)

func TestGoSourceS319MappedGenericDescriptorNames(t *testing.T) {
	program, err := gosource.Load([]gosource.Source{{Name: "main.go", Data: []byte(`package main
import "test/p"
func main() { p.Run() }
`)}}, gosource.Options{
		RunMain: true,
		Packages: []gosource.PackageSpec{{Path: "test/p", Sources: []gosource.Source{{Name: "p.go", Data: []byte(`package p
type sparseKey interface { ~int | ~int32 }
type sparseEntry[K sparseKey, V any] struct { key K; val V }
type genericSparseMap[K sparseKey, V any] struct { dense []sparseEntry[K, V]; sparse []int32 }
type sparseMap = genericSparseMap[int, int32]
func entries() []sparseEntry[int, int32] { return nil }
func Run() { var m sparseMap; _, _ = m, entries() }
`)}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := &Runner{bashPPGoSource: true, bashPPGoSourceFile: program.File}
	locals := runner.bashPPLocalTypeDescriptors()
	entries := bashPPNativeLocalTypeEntries(locals)
	for _, typ := range runner.bashPPBuildGenericBridgeTypes() {
		if !entries[typ] {
			t.Fatalf("generic bridge type %q was not matched to its materialised helper; entries=%v", typ, entries)
		}
	}
}
