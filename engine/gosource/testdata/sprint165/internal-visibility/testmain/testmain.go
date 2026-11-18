// cmd/go's generated _testmain.go imports testing/internal/testdeps; cmd/go
// admits that import for the testmain alone. Bash++ receives the fact as
// Options.TestMain; without it the same identity is refused.
package main

import (
	"os"
	"testing"
	"testing/internal/testdeps"
)

var tests = []testing.InternalTest{}

func main() {
	m := testing.MainStart(testdeps.TestDeps{}, tests, nil, nil, nil)
	os.Exit(m.Run())
}
