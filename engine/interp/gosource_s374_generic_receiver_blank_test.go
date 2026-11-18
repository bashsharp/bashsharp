//go:build full

package interp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
)

// A generic method receiver may use '_' or omit unused type parameters
// (e.g. `func (l *List[_]) Len() int`). These are receiver type parameters,
// not concrete generic type arguments, and '_' is never a concrete type.
// Previously, generic bridge type collection treated them as concrete
// instantiations and registered them as generic bridge types, emitting
// `reflect.TypeFor[List[_]]()` in the dependency bridge worker, which failed
// Go compilation with "cannot use _ as value or type".
func TestS374GenericReceiverBlankBridgeType(t *testing.T) {
	goroot := os.Getenv("HOME") + "/sdk/go1.27.1"
	list2Src, err := os.ReadFile(filepath.Join(goroot, "test/typeparam/list2.go"))
	if err != nil {
		t.Skipf("skipping: %v", err)
	}
	program, err := gosource.Load([]gosource.Source{{Name: "list2.go", Data: list2Src}}, gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	runner := &Runner{bashPPGoSource: true, bashPPGoSourceFile: program.File}
	bridgeTypes := runner.bashPPBuildGenericBridgeTypes()
	for _, typ := range bridgeTypes {
		if typ == "_List[_]" {
			t.Fatalf("unexpected blank generic bridge type %q in generic bridge types: %v", typ, bridgeTypes)
		}
	}
}
