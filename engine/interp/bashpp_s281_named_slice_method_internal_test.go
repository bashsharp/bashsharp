//go:build full

package interp

// Sprint: #281; Story: #809; Story-ID: fac7e14af4a8

import (
	"slices"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
)

// TestS281NamedSliceVariadicMethodMirror verifies that an ordinary variadic
// method is represented by the dependency helper instead of falling into the
// broad OmittedMethods refusal. Its generated body remains only a callback
// stub; the original append body is never compiled or executed natively.
func TestS281NamedSliceVariadicMethodMirror(t *testing.T) {
	const source = `package main
type Nodes []int
func (n *Nodes) Append(values ...int) { *n = append(*n, values...) }
`
	program, err := gosource.Parse(strings.NewReader(source), "nodes.go", gosource.Options{})
	if err != nil {
		t.Fatal(err)
	}
	runner := &Runner{bashPPGoSource: true, bashPPGoSourceFile: program.File}
	descriptors := runner.bashPPLocalTypeDescriptors()
	local := slices.IndexFunc(descriptors, func(typ bashPPLocalType) bool { return typ.Name == "Nodes" })
	if local < 0 {
		t.Fatal("Nodes was not materialised")
	}
	descriptor := descriptors[local]
	if slices.Contains(descriptor.OmittedMethods, "Append") {
		t.Fatalf("Append stayed omitted: %+v", descriptor)
	}
	if len(descriptor.Methods) != 1 {
		t.Fatalf("methods = %+v, want one Append mirror", descriptor.Methods)
	}
	method := descriptor.Methods[0]
	if method.Name != "Append" || !method.Pointer || !method.Variadic || !method.General || !slices.Equal(method.Params, []string{"int"}) || len(method.Results) != 0 {
		t.Fatalf("Append descriptor = %+v", method)
	}
	generated := bashPPLocalTypeGo(descriptor)
	if !strings.Contains(generated, "Append(bpparg0 ...int)") || !strings.Contains(generated, "encode(reflect.ValueOf(bpparg0))") {
		t.Fatalf("variadic callback stub not generated:\n%s", generated)
	}
}
