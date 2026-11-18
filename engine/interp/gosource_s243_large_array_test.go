//go:build full

package interp_test

import "testing"

// TestGoSourceIssue34395 keeps the unchanged Go 1.27.1 regression program on
// the interpreted path. In particular, this is a dense array value with one
// explicit element; the remaining elements must retain Go's byte zero value.
func TestGoSourceIssue34395(t *testing.T) {
	const source = `package main

var test = [100 * 1024 * 1024]byte{42}

func main() {
	if test[0] != 42 {
		panic("bad")
	}
}`
	_, stderr, err := runGoSource(t, "issue34395", source)
	if err != nil {
		t.Fatalf("interpreted issue34395: %v\nstderr:\n%s", err, stderr)
	}
}

func TestGoSourceLargeArrayImplicitZeros(t *testing.T) {
	const source = `package main

var test = [8 * 1024 * 1024]byte{0: 42, 4 * 1024 * 1024: 7, 8 * 1024 * 1024 - 1: 9}

func main() {
	if test[0] != 42 || test[1] != 0 || test[4 * 1024 * 1024 - 1] != 0 ||
		test[4 * 1024 * 1024] != 7 || test[4 * 1024 * 1024 + 1] != 0 ||
		test[8 * 1024 * 1024 - 2] != 0 || test[8 * 1024 * 1024 - 1] != 9 {
		panic("bad sparse array contents")
	}
}`
	_, stderr, err := runGoSource(t, "large-array-implicit-zeros", source)
	if err != nil {
		t.Fatalf("interpreted large array: %v\nstderr:\n%s", err, stderr)
	}
}
