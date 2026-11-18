package shellrt_test

import (
	"errors"
	"testing"

	"github.com/bashsharp/bashsharp/engine/lower/shellrt"
)

func TestSourceRecoverPreservesCheckedAbort(t *testing.T) {
	failure := &shellrt.ValueError{Code: "BASHPP-EASSERT-FAIL", Message: "checked failure"}
	for _, payload := range []any{shellrt.ValueAbort{Err: failure}, shellrt.ChannelAbort{Err: errors.New("cancelled")}, "source panic"} {
		var escaped, caught any
		func() {
			defer func() { escaped = recover() }()
			func() { defer func() { caught = shellrt.PreserveAbort(recover()) }(); panic(payload) }()
		}()
		if payload == "source panic" {
			if caught != payload || escaped != nil {
				t.Fatalf("source panic escaped: %v / %v", caught, escaped)
			}
		} else if escaped != payload || caught != nil {
			t.Fatalf("guard consumed: %v / %v", caught, escaped)
		}
	}
}

type guardReader interface{ Read() int }
type guardSame interface {
	Read() int
	Close()
}
type guardConflict interface{ Read() string }
type guardConcrete int

func (guardConcrete) Read() int { return 0 }

func TestAssertionMethodSetClassification(t *testing.T) {
	if !shellrt.AssertionPossible[guardReader, guardConcrete]() || !shellrt.AssertionPossible[guardReader, guardSame]() || shellrt.AssertionPossible[guardReader, guardConflict]() || shellrt.AssertionPossible[guardReader, int]() {
		t.Fatal("incorrect assertion possibility")
	}
}
