//go:build full

// Sprint: #209; Story: #460; Story-ID: d8e7d58f362b
//
// String slice bounds: an out-of-range string slice whose bound is a runtime
// value faults as Go's recoverable slice-bounds panic (corpus roots
// fixedbugs/issue29504 and fixedbugs/issue30116, which slice strings by a
// runtime index and recover), matching the array/slice path. A wholly constant
// invalid slice stays the Go-source checker's compile-time error and never
// reaches the recoverable panic path.
package interp_test

import (
	"strings"
	"testing"

	"github.com/go-quicktest/qt"

	"github.com/bashsharp/bashsharp/engine/gosource"
)

func TestStory460StringSliceBoundsPanic(t *testing.T) {
	// Positive: a runtime low bound past the length recovers with Go's message.
	src := "package main\nimport \"fmt\"\nfunc main() {\n\tdefer func() {\n\t\tif r := recover(); r != nil {\n\t\t\tfmt.Println(\"recovered:\", r)\n\t\t}\n\t}()\n\ts := \"foo\"\n\ti := 9\n\t_ = s[i:]\n\tfmt.Println(\"unreachable\")\n}\n"
	out, stderr, err := runGoSource(t, "story460strslicelow", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "recovered: runtime error: slice bounds out of range [9:3]\n"))
}

func TestStory460StringSliceHighBoundsPanic(t *testing.T) {
	// Positive: a runtime high bound past the length reports the length form.
	src := "package main\nimport \"fmt\"\nfunc main() {\n\tdefer func() {\n\t\tif r := recover(); r != nil {\n\t\t\tfmt.Println(\"recovered:\", r)\n\t\t}\n\t}()\n\ts := \"foo\"\n\ti := 9\n\t_ = s[:i]\n\tfmt.Println(\"unreachable\")\n}\n"
	out, stderr, err := runGoSource(t, "story460strslicehigh", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "recovered: runtime error: slice bounds out of range [:9] with length 3\n"))
}

func TestStory460StringSliceConstInvalidRejected(t *testing.T) {
	// Negative: a constant string sliced with constant out-of-range bounds is a
	// compile-time error the Go-source front end reports; it must never be
	// admitted and turned into a recoverable panic.
	src := "package main\nfunc main() {\n\t_ = \"foo\"[0:5]\n}\n"
	_, err := gosource.Parse(strings.NewReader(src), "story460strconst.go", gosource.Options{RunMain: true})
	qt.Assert(t, qt.IsNotNil(err))
	qt.Assert(t, qt.IsTrue(strings.Contains(err.Error(), "out of bounds")), qt.Commentf("err: %v", err))
}
