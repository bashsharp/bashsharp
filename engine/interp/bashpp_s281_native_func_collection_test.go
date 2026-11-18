//go:build full

package interp_test

import (
	"testing"
)

// TestS281NativeFuncInCollection exercises a native (module-imported, NOT
// interpreted) dependency whose function values are stored in and retrieved
// from a map keyed by a dependency package variable.  The classic compiler
// shape is `archInits[GOARCH]` selecting an `Init` handle that is then passed
// to `Main`.  Retrieving a native `func(*dep.ArchInfo)` from a collection must
// yield the function handle, not misread it as a scalar string.
func TestS281NativeFuncInCollection(t *testing.T) {
	dep := `package dep

import "fmt"

type ArchInfo struct{ Name string }

var GOARCH = "amd64"

func Init(a *ArchInfo) { a.Name = "amd64" }

func Main(fn func(*ArchInfo)) {
	var a ArchInfo
	fn(&a)
	fmt.Println(a.Name)
}`
	main := `package main

import (
	"fmt"

	"example.com/nativefunccoll/dep"
)

func main() {
	archInits := map[string]func(*dep.ArchInfo){
		"amd64": dep.Init,
	}
	archInit, ok := archInits[dep.GOARCH]
	fmt.Println(ok)
	dep.Main(archInit)
}`
	differGoSourceDependencyModule(t, "example.com/nativefunccoll", dep, main)
}
