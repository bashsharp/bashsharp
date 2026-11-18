// A test main that ALSO imports a top-level internal package: TestMain
// exempts testing/internal/… only; internal/testenv still needs a standard
// identity, so cmd/internal/testdir.test is admitted and example.com/m.test
// is refused.
package main

import (
	"internal/testenv"
	"testing"
	"testing/internal/testdeps"
)

var _ = testenv.Builder
var _ = testing.MainStart
var _ testdeps.TestDeps
