//go:build full

package interp_test

// Sprint: #165; Story: #99; Story-ID: ca559d7ee23d
//
// A closure declared in a block shadows a same-named package function.

import (
	"os"
	"path/filepath"
	"testing"
)

func mustReadSprint165ClosureShadow(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "sprint165", "runtime-2", "closure-shadow", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// closure.go's shape: `f := func() {…}; f()` inside package function f, in a
// void function, in a function returning a value and with an argument. The
// call runs the local closure once; outside its block the package function
// is the callee again. Before the mechanism this recursed without bound.
func TestGoSourceSprint165ScopedClosureShadowsPackageFunc(t *testing.T) {
	differGoSource(t, mustReadSprint165ClosureShadow(t, "local_shadows_func.go.txt"), nil, "")
}

// Positive control: a package function called from a block that declares a
// same-named NON-function local is still the package function's caller's
// concern in Go (a compile error there), so the control is the supported
// form — a local closure whose name shadows nothing and a package function
// with a distinct name called from inside it.
func TestGoSourceSprint165ScopedClosureDistinctNames(t *testing.T) {
	differGoSource(t, mustReadSprint165ClosureShadow(t, "distinct_names.go.txt"), nil, "")
}

// Negative: a closure declared in an INNER block goes out of scope with it;
// after the block the package function is the callee again (wrong-value if
// the shadow outlived its block), and a closure held in a package-level
// variable is not a shadow of anything (it cannot share the name).
func TestGoSourceSprint165ScopedClosureBlockExit(t *testing.T) {
	differGoSource(t, mustReadSprint165ClosureShadow(t, "block_exit.go.txt"), nil, "")
}
