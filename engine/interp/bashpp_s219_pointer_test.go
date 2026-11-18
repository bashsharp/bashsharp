//go:build full

package interp_test

import (
	"strings"
	"testing"

	"github.com/go-quicktest/qt"
)

// Pointer-to-array indexing has the same implicit dereference in an address
// expression as it does in a read or write: &p[i] is &(*p)[i]. This is the
// ordinary typed-pointer portion shared by nilptr2.go and issue49295.go.
func TestS219PointerArrayIndexAddress(t *testing.T) {
	src := `package main
func main() {
	a := [3]int{2, 4, 6}
	p := &a
	q := &p[1]
	*q = 9
	println(a[1], *q)
}`
	out, stderr, err := runGoSource(t, "s219-pointer-array-index", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(out, ""))
	qt.Assert(t, qt.Equals(stderr, "9 9\n"))
}

// A nil pointer-to-array still faults when the implicit dereference is
// evaluated. Merely supporting p[i] must not turn nil storage into an array.
func TestS219PointerArrayIndexAddressNil(t *testing.T) {
	src := `package main
func main() {
	var p *[3]int
	_ = &p[0]
}`
	_, stderr, err := runGoSource(t, "s219-pointer-array-index-nil", src)
	qt.Assert(t, qt.IsNotNil(err))
	qt.Assert(t, qt.IsTrue(strings.Contains(stderr, "nil pointer dereference")),
		qt.Commentf("stderr: %s", stderr))
}

// A generic conversion from a slice to a defined pointer-to-array type is
// ordinary Go pointer construction and aliases the slice's backing storage.
func TestS219PointerGenericSliceToDefinedArrayPointer(t *testing.T) {
	src := `package main
type Token *[4]byte
func take[T interface{ ~*[4]byte }](s []byte) T { return T(s[:4]) }
func main() {
	b := []byte("abcd")
	p := take[Token](b)
	p[2] = 'X'
	println(string(b))
}`
	out, stderr, err := runGoSource(t, "s219-pointer-generic-slice-array", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(out, ""))
	qt.Assert(t, qt.Equals(stderr, "abXd\n"))
}

// Slice bounds in a slice-to-array-pointer conversion are evaluated once in
// Go order. The shortened view controls the conversion length, while the
// resulting pointer continues to alias the operand's backing storage.
func TestS219PointerSliceBoundsOnceAndAlias(t *testing.T) {
	src := `package main
var calls int
func low() int { calls = calls*10 + 1; return 0 }
func high() int { calls = calls*10 + 2; return 4 }
func main() {
	b := []byte("abcdef")
	p := (*[4]byte)(b[low():high()])
	p[1] = 'X'
	println(calls, string(b))
}`
	out, stderr, err := runGoSource(t, "s219-pointer-slice-bounds-once", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(out, ""))
	qt.Assert(t, qt.Equals(stderr, "12 aXcdef\n"))
}

func TestS219PointerSliceHighControlsConversionLength(t *testing.T) {
	src := `package main
func low() int { println("low"); return 0 }
func high() int { println("high"); return 4 }
func main() {
	b := []byte("abcdef")
	_ = (*[5]byte)(b[low():high()])
}`
	_, stderr, err := runGoSource(t, "s219-pointer-slice-high-length", src)
	qt.Assert(t, qt.IsNotNil(err))
	qt.Assert(t, qt.Equals(strings.Count(stderr, "low\n"), 1), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(strings.Count(stderr, "high\n"), 1), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.IsTrue(strings.Contains(stderr, "cannot convert slice with length 4")),
		qt.Commentf("stderr: %s", stderr))
}

func TestS219PointerNestedSliceViewLengthCapacity(t *testing.T) {
	src := `package main
func main() {
	b := []byte("abcdef")
	v := b[:4:4][:]
	println(len(v), cap(v))
	p := (*[4]byte)(b[:4:4][:])
	p[3] = 'X'
	println(string(b))
}`
	out, stderr, err := runGoSource(t, "s219-pointer-nested-slice-view", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(out, ""))
	qt.Assert(t, qt.Equals(stderr, "4 4\nabcXef\n"))
}

func TestS219PointerSliceBoundsCaptureOriginalBacking(t *testing.T) {
	src := `package main
var b []byte
var original []byte
func high() int {
	b = []byte("uvwxyz")
	return 4
}
func main() {
	b = []byte("abcdef")
	original = b
	p := (*[4]byte)(b[:high()])
	p[1] = 'X'
	println(string(original), string(b))
}`
	out, stderr, err := runGoSource(t, "s219-pointer-bounds-rebind", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(out, ""))
	qt.Assert(t, qt.Equals(stderr, "aXcdef uvwxyz\n"))
}

func TestS219PointerSliceConversionSurvivesRebind(t *testing.T) {
	src := `package main
func main() {
	b := []byte("abcdef")
	original := b
	p := (*[4]byte)(b[:4])
	b = []byte("uvwxyz")
	p[2] = 'X'
	println(string(original), string(b))
}`
	out, stderr, err := runGoSource(t, "s219-pointer-post-conversion-rebind", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(out, ""))
	qt.Assert(t, qt.Equals(stderr, "abXdef uvwxyz\n"))
}

// unsafe.Pointer reinterpretation is intentionally outside this mechanism.
// Keep the boundary loud instead of accidentally treating its integer-like
// carrier as interpreter-owned typed storage.
func TestS219PointerUnsafeReinterpretationRejected(t *testing.T) {
	src := `package main
import "unsafe"
func main() {
	x := 1
	_ = *(*byte)(unsafe.Pointer(&x))
}`
	_, stderr, err := runGoSource(t, "s219-pointer-unsafe-boundary", src)
	qt.Assert(t, qt.IsNotNil(err))
	qt.Assert(t, qt.IsTrue(strings.Contains(stderr, "BASHPP-")),
		qt.Commentf("stderr: %s", stderr))
}

func TestS219PointerTargetParallelCallResults(t *testing.T) {
	src := `package main
var calls int
func escaped(x int) *int { calls++; var v int; v = x; return &v }
func main() {
	p, q := escaped(1), escaped(2)
	p, q = escaped(3), escaped(4)
	*p = 7
	println(*p, *q, calls)
}`
	out, stderr, err := runGoSource(t, "s219-pointer-target-parallel-calls", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(out, ""))
	qt.Assert(t, qt.Equals(stderr, "7 4 4\n"))
}

func TestS219PointerTargetGenericDefinedArrayPointer(t *testing.T) {
	src := `package main
type Token *[4]byte
func take[T interface{ ~*[4]byte }](buf []byte) T { return T(buf[:4]) }
func main() {
	buf := []byte("abcd")
	t := take[Token](buf)
	t[1] = 'Z'
	println(string(buf))
}`
	out, stderr, err := runGoSource(t, "s219-pointer-target-generic-array", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(out, ""))
	qt.Assert(t, qt.Equals(stderr, "aZcd\n"))
}

func TestS219PointerTargetLaterPackageStorage(t *testing.T) {
	src := `package main
func addr() *int { return &g }
var g int
func main() { p := addr(); *p = 9; println(g) }`
	out, stderr, err := runGoSource(t, "s219-pointer-target-later-global", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(out, ""))
	qt.Assert(t, qt.Equals(stderr, "9\n"))
}

func TestS219PointerTargetNilGenericArrayPointer(t *testing.T) {
	src := `package main
type Token *[1]byte
func main() { var buf []byte; p := Token(buf); p[0] = 1 }`
	_, stderr, err := runGoSource(t, "s219-pointer-target-nil-array", src)
	qt.Assert(t, qt.IsNotNil(err))
	qt.Assert(t, qt.StringContains(stderr, "nil pointer dereference"))
}

// A slice-to-array pointer is an addressable view of the slice backing store,
// not a detached array cell. Whole-array assignment must first snapshot the
// right operand, then write through the view, including when the two views
// overlap in either direction. Range over an array pointer likewise evaluates
// the producing call once and reads the pointed-to elements.
func TestS219ArrayPointerStorage(t *testing.T) {
	src := `package main
var calls int
func array() *[5]int { calls++; return &[5]int{1, 2, 3, 4, 5} }
func main() {
	forward := []byte("0123456789")
	*(*[6]byte)(forward[2:8]) = *(*[6]byte)(forward[0:6])
	println(string(forward))

	backward := []byte("0123456789")
	*(*[6]byte)(backward[0:6]) = *(*[6]byte)(backward[2:8])
	println(string(backward))

	sum := 0
	for _, value := range array() { sum += value }
	println(sum, calls)
}`
	out, stderr, err := runGoSource(t, "s219-array-pointer-storage", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(out, ""))
	qt.Assert(t, qt.Equals(stderr, "0101234589\n2345676789\n15 1\n"))
}
