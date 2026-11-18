package lib_test

import (
	"slices"
	"testing"

	. "example.com/xtp/lib"
)

func TestSort(t *testing.T) {
	data := []*Sym{{3}, {1}, {2}}
	slices.SortFunc(data, CmpN)
	if data[0].N != 1 {
		t.Fatal(data[0].N)
	}
}
