//go:build full

package lower_test

import (
	"testing"

	"github.com/bashsharp/bashsharp/engine/lower"
)

// A package qualifier is not a value that can be parenthesized. The recipe
// smoke's os.Args expression must remain a Go package selector during lowering.
func TestBashSharpImportedPackageVariable(t *testing.T) {
	const source = `import "os"
func recipeSmoke() {
    args := os.Args
    printf '%s' args
}

recipeSmoke()
`
	if _, err := lower.Compile(parse(t, source, "recipe.bsh"), lower.Options{}); err != nil {
		t.Fatal(err)
	}
}

func TestBashSharpImportedPackageVariableAliasAndShadow(t *testing.T) {
	const source = `import process "os"
type Holder struct { Args string }
func read(process *Holder) string {
    value := process.Args
    return value
}
func recipeSmoke() {
    args := process.Args
    first := args[1]
    printf '%s\n' first
    process := new(Holder)
    process.Args = "local"
    value := process.Args
    printf '%s\n' value
    parameter := read(process)
    printf '%s\n' parameter
}
recipeSmoke()
`
	result, err := lower.Compile(parse(t, source, "recipe.bsh"), lower.Options{})
	if err != nil {
		t.Fatal(err)
	}
	out, stderr, status := runArtifact(t, result, "argument with spaces")
	if status != 0 || stderr != "" || out != "argument with spaces\nlocal\nlocal\n" {
		t.Fatalf("status=%d out=%q stderr=%q", status, out, stderr)
	}
}
