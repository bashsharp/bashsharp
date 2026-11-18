package lib_test

import (
	"slices"
	"testing"

	"example.com/xtp/lib"
)

// The same external package, importing the tested package by name: the
// instantiation's spelled arguments must carry the qualifier (lib.Sym).
func TestSortNamed(t *testing.T) {
	data := []*lib.Sym{{3}, {1}, {2}}
	slices.SortFunc(data, lib.CmpN)
	if data[0].N != 1 {
		t.Fatal(data[0].N)
	}
}
