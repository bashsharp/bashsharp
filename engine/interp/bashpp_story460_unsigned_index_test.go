//go:build full

package interp_test

import (
	"strings"
	"testing"

	"github.com/go-quicktest/qt"

	"github.com/bashsharp/bashsharp/engine/interp"
)

// TestStory460UnsignedCollectionIndexes is an outside-corpus reduction of the
// unsigned-index failures shared by generated bounds tests and indexing with
// unsafe.Sizeof. A valid uint64 index that exceeds int64 must reach Go's
// recoverable bounds panic; it is still an integer index.
func TestStory460UnsignedCollectionIndexes(t *testing.T) {
	differGoSource(t, `package main

import (
	"fmt"
	"unsafe"
)

func caught(f func()) (result any) {
	defer func() { result = recover() }()
	f()
	return nil
}

func bySize[T byte](value T) {
	text := "x"
	fmt.Println(caught(func() { _ = text[unsafe.Sizeof(value)] }))
}

func main() {
	i := ^uint64(0)
	text := "abc"
	array := [3]byte{}
	slice := []byte{1, 2, 3}
	fmt.Println(caught(func() { _ = text[i] }))
	fmt.Println(caught(func() { _ = array[i] }))
	fmt.Println(caught(func() { _ = slice[i] }))
	fmt.Println(caught(func() { _ = slice[:i] }))
	fmt.Println(caught(func() { _ = slice[i:] }))
	fmt.Println(caught(func() { _ = slice[:1:i] }))
	fmt.Println(caught(func() { _ = text[:i] }))
	fmt.Println(caught(func() { _ = text[i:] }))
	bySize(byte(0))
}
`, nil, "")
}

func TestStory460ClassicUnsignedIndexRejected(t *testing.T) {
	_, stderr, err := runBashSharpCall(t, `func main() {
	x := []int{1}
	i := 18446744073709551615
	_ = x[i]
}
main()
`)
	qt.Assert(t, qt.ErrorIs(err, interp.ExitStatus(2)))
	qt.Assert(t, qt.IsTrue(strings.Contains(stderr, "BASHPP-ECOLLECTION-INDEX: index must be an integer")), qt.Commentf("stderr: %s", stderr))
}
