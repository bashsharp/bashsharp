// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"runtime"
	"strings"
	"sync"
	"testing"
	"weak"

	"github.com/bashsharp/bashsharp/engine/gosource"
)

func TestS281SourceTypeIndexPreservesScopedAndGenericDescriptors(t *testing.T) {
	program, err := gosource.Parse(strings.NewReader(`package main
import "fmt"
type Box[T any] struct { Value T }
var _ = Box[int]{Value: 1}
type Item struct { Package int }
func first() { type Item struct { First string }; fmt.Println(Item{}) }
func second() { type Item struct { Second bool }; fmt.Println(Item{}) }
`), "source_index.go", gosource.Options{})
	if err != nil {
		t.Fatal(err)
	}
	runner := &Runner{
		bashPPGoSource:     true,
		bashPPGoSourceFile: program.File,
		bashPPImports:      map[string]string{"fmt": "fmt"},
	}
	types, scoped := runner.bashPPBuildLocalTypeDescriptors()
	if len(scoped) != 2 {
		t.Fatalf("scoped local identities = %v, want two Item declarations", scoped)
	}
	scopedDescriptors := map[string]bool{}
	for _, name := range scoped {
		scopedDescriptors[name] = false
	}
	packageItem, genericInstance := false, false
	for _, descriptor := range types {
		if descriptor.Name == "Item" {
			packageItem = true
		}
		if _, ok := scopedDescriptors[descriptor.Name]; ok {
			scopedDescriptors[descriptor.Name] = true
		}
		if descriptor.PublicType == "Box[int]" {
			genericInstance = true
		}
	}
	if !packageItem || !genericInstance {
		t.Fatalf("package Item=%v generic Box[int]=%v; descriptors=%+v", packageItem, genericInstance, types)
	}
	for name, found := range scopedDescriptors {
		if !found {
			t.Errorf("scoped descriptor %q was not materialised", name)
		}
	}
	selectors := runner.bashPPReferencedSelectors()
	if len(selectors) != 1 || selectors[0] != "fmt.Println" {
		t.Fatalf("referenced selectors = %v, want [fmt.Println]", selectors)
	}
}

func TestS281SourceTypeIndexScopeOwnerRetainsParentAcrossGC(t *testing.T) {
	program, err := gosource.Parse(strings.NewReader(`package main
func one() { type Local struct { N int }; _ = Local{} }
`), "scope_owner.go", gosource.Options{})
	if err != nil {
		t.Fatal(err)
	}
	owner := &Runner{bashPPGoSource: true, bashPPGoSourceFile: program.File}
	localTypes := owner.goSourceLocalTypes()
	if localTypes == nil || owner.goSourceLocalTypes() != localTypes {
		t.Fatal("scope-only owner did not retain its local type index")
	}

	// Observe the shared parent without retaining it here. Its only strong
	// owner across the collection must be the Runner above; the global entry
	// deliberately holds it weakly.
	parentRef := weak.Make(bashPPScanLocalTypeDecls(program.File))
	runtime.GC()
	parent := parentRef.Value()
	if parent == nil {
		t.Fatal("scope-only owner allowed the shared parent index to be collected")
	}
	if owner.goSourceLocalTypes() != localTypes {
		t.Fatal("scope-only owner lost its local type index across collection")
	}
	consumer := &Runner{bashPPGoSource: true, bashPPGoSourceFile: program.File}
	_, _ = consumer.bashPPBuildLocalTypeDescriptors()
	later := consumer.bashPPLocalTypeDeclarationIndex()
	if later != parent {
		t.Fatal("later consumer did not reuse the parent index retained by the scope-only owner")
	}
	if later.localTypes != localTypes {
		t.Fatal("later parent index does not contain the scope owner's local type index")
	}
	runtime.KeepAlive(owner)
}

func TestS281SourceTypeIndexSingleScanAcrossConsumers(t *testing.T) {
	program, err := gosource.Parse(strings.NewReader(`package main
type Pair[T any] struct { Left, Right T }
func one() { type Local struct { N int }; _ = Pair[Local]{} }
func two() { type Local struct { S string }; _ = Pair[Local]{} }
`), "single_scan.go", gosource.Options{})
	if err != nil {
		t.Fatal(err)
	}
	const copies = 12
	indexes := make(chan *bashPPLocalTypeDeclCache, copies)
	var wg sync.WaitGroup
	for range copies {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runner := &Runner{bashPPGoSource: true, bashPPGoSourceFile: program.File}
			index := runner.bashPPLocalTypeDeclarationIndex()
			_ = runner.bashPPLocalTypeRenderer()
			_ = runner.goSourceLocalTypes()
			_ = runner.bashPPBuildGenericBridgeTypes()
			_, _ = runner.bashPPBuildLocalTypeDescriptors()
			indexes <- index
		}()
	}
	wg.Wait()
	close(indexes)
	var shared *bashPPLocalTypeDeclCache
	for index := range indexes {
		if shared == nil {
			shared = index
		}
		if index != shared {
			t.Fatal("copied runners did not share the immutable source index")
		}
	}
	if shared == nil {
		t.Fatal("source index was not built")
	}
	if shared.fullScans != 1 {
		t.Fatalf("full-file scan count = %d, want 1", shared.fullScans)
	}
}
