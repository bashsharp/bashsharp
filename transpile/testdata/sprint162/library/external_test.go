package library_test

import (
	"example/library"
	"testing"
)

func TestValue(t *testing.T) {
	if !library.Initialized || library.Value() != 42 {
		t.Fatal("bad")
	}
}
