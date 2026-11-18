//go:build full

package interp_test

// Sprint: #209; Story: #461; Story-ID: 4ed649697945

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/go-quicktest/qt"
)

// Outside-corpus coverage for the root-pointer form accepted by Go's
// unsafe.Offsetof. The promoted field is reached through a value-embedded
// struct, so its offset is still a valid constant.
func TestStory461UnsafeOffsetofPointerStructSelector(t *testing.T) {
	src := `package main
import ("fmt"; "unsafe")
type Embedded struct { B int64 }
type S struct { A byte; Embedded }
func main() { d := new(S); fmt.Println(unsafe.Offsetof(d.B), unsafe.Offsetof(d.Embedded)) }
`
	out, stderr, err := runGoSource(t, "story461offsetofpointer", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "8 8\n"))
}

// The same outer-pointer rule applies to an instantiated generic struct.
func TestStory461UnsafeOffsetofGenericPointerSelector(t *testing.T) {
	src := `package main
import ("fmt"; "unsafe")
type Embedded struct { B int }
type S[K any] struct { A K; Embedded }
func show[K any](d *S[K]) { fmt.Println(unsafe.Offsetof(d.B), unsafe.Offsetof(d.Embedded)) }
func main() { show(new(S[int])) }
`
	out, stderr, err := runGoSource(t, "story461offsetofgeneric", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "8 8\n"))
}

// An embedded pointer remains invalid even when the root receiver is a value:
// Go cannot express a stable offset through that pointer indirection.
func TestStory461UnsafeOffsetofRejectsEmbeddedPointer(t *testing.T) {
	src := `package main
import "unsafe"
type Embedded struct { B int64 }
type S struct { *Embedded }
func main() { var s S; _ = unsafe.Offsetof(s.B) }
`
	_, err := gosource.Parse(strings.NewReader(src), "embedded_pointer.go", gosource.Options{RunMain: true})
	qt.Assert(t, qt.ErrorMatches(err, `.*field B is embedded via a pointer in S.*`))
}
