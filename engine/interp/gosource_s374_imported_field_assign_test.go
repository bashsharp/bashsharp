//go:build full

package interp_test

import "testing"

// A field assignment whose operand is a dependency package variable writes
// that variable's own storage. The operand used to be read as a value, so the
// write landed in a copy and was silently lost; a later read through the
// unset pointer field then raised a nil dereference.
func TestS374ImportedVariableFieldAssign(t *testing.T) {
	dep := `package dep

type Arch struct {
	Name    string
	PtrSize int
}

type LinkArch struct {
	*Arch
	Family int
}

type Info struct {
	LinkArch *LinkArch
	REGSP    int
	MAXWIDTH int64
	Tags     []string
	Inner    struct{ X, Y int }
}

var AMD64 = LinkArch{Arch: &Arch{Name: "amd64", PtrSize: 8}, Family: 3}

var Config Info

var Ctxt *Info

func Width() int64 { return Config.MAXWIDTH }

func PtrSize() int {
	if Config.LinkArch == nil {
		return -1
	}
	return Config.LinkArch.PtrSize
}`
	main := `package main

import (
	"fmt"

	"example.com/fieldassign/dep"
)

func main() {
	dep.Config.LinkArch = &dep.AMD64
	dep.Config.REGSP = 5
	dep.Config.MAXWIDTH = 1 << 50
	fmt.Println(dep.Config.LinkArch != nil, dep.Config.REGSP, dep.Config.MAXWIDTH, dep.Width())
	fmt.Println(dep.Config.LinkArch.PtrSize, dep.Config.LinkArch.Name, dep.PtrSize())

	dep.Config.Inner.X = 7
	(dep.Config).Inner.Y = 9
	fmt.Println(dep.Config.Inner.X, dep.Config.Inner.Y)

	dep.Config.Tags = append(dep.Config.Tags, "a", "b")
	fmt.Println(dep.Config.Tags, len(dep.Config.Tags))

	dep.Ctxt = &dep.Info{}
	dep.Ctxt.REGSP = 11
	dep.Ctxt.Inner.X = 13
	fmt.Println(dep.Ctxt.REGSP, dep.Ctxt.Inner.X)

	dep.AMD64.Family = 4
	dep.AMD64.Arch.PtrSize = 4
	fmt.Println(dep.AMD64.Family, dep.Config.LinkArch.Family, dep.PtrSize())

	defer func() { fmt.Println("recovered:", recover()) }()
	dep.Ctxt = nil
	dep.Ctxt.REGSP = 1
}`
	differGoSourceDependencyModule(t, "example.com/fieldassign", dep, main)
}

// Compound assignment and increment on a field of an imported package variable
// must use the same by-address base as plain assignment. Before the fix the
// update path reported BASHPP-EUPDATE-TARGET: BASHPP-EPOINTER-TARGET.
func TestS374ImportedVariableFieldCompound(t *testing.T) {
	source := `package main

import (
	"fmt"
	"go/build"
	"image"
)

func main() {
	orig := build.Default.GOARCH
	fmt.Println("before", build.Default.GOARCH)
	build.Default.GOARCH += "q"
	fmt.Println("after", build.Default.GOARCH)
	build.Default.GOARCH = orig
	fmt.Println("restore", build.Default.GOARCH)

	fmt.Println("zp before", image.ZP.X, image.ZP.Y)
	image.ZP.X += 3
	fmt.Println("zp add", image.ZP.X, image.ZP.Y)
	image.ZP.X++
	fmt.Println("zp inc", image.ZP.X, image.ZP.Y)
	image.ZP.X--
	fmt.Println("zp dec", image.ZP.X, image.ZP.Y)
	// reset
	image.ZP.X = 0
	fmt.Println("zp reset", image.ZP.X, image.ZP.Y)
}`
	differGoSource(t, source, nil, "")
}
