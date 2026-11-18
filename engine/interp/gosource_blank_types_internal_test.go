package interp

import (
	"context"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
)

func TestGoSourceBlankTypeBridgeRegistration(t *testing.T) {
	program, err := gosource.Parse(strings.NewReader(`package main
type _ struct { Inner struct { Value int } }
type Named int
func main() {}
`), "blank.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	r := &Runner{bashPPGoSourceFile: program.File}
	descriptors, _ := r.bashPPBuildLocalTypeDescriptors()
	named, nested := false, false
	for _, descriptor := range descriptors {
		if descriptor.Name == "_" {
			t.Fatal("blank type registered")
		}
		named = named || descriptor.Name == "Named"
		nested = nested || (descriptor.Alias && descriptor.WireType == "struct{Value int}")
	}
	if !named || !nested {
		t.Fatalf("named=%v nested=%v descriptors=%+v", named, nested, descriptors)
	}
	source, err := bashPPNativeSource(context.Background(), bashPPEvalRequest{LocalTypes: descriptors})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"reflect.TypeFor[_]", "reflect.TypeFor[*_]", "type _ "} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("generated worker contains %q", forbidden)
		}
	}
}
