package shellrt

import (
	"errors"
	"math"
	"testing"
)

type checkedReader interface{ Read() int }
type checkedBox struct{ N int }

func (b *checkedBox) Read() int { return b.N }

type checkedValueBox struct{ N int }

func (b checkedValueBox) Read() int { return b.N }

func TestCheckedValueIdentity(t *testing.T) {
	site := ValueSite{File: "values.bpp", Name: "p", Line: 3, Column: 8, Offset: 24}
	var pointer *int
	_, err := Deref(pointer, site)
	var valueError *ValueError
	if !errors.As(err, &valueError) || valueError.ExitStatus() != 2 || valueError.Site != site || err.Error() != "BASHPP-ENIL-DEREF: dereference of nil pointer" {
		t.Fatalf("error=%#v", err)
	}
	number := 7
	location := MustValue(CheckedPointer(&number, site))
	*location = 9
	if number != 9 {
		t.Fatal("checked location lost identity")
	}
	array := [1]int{4}
	copied := MustValue(Deref(&array, site))
	copied[0] = 8
	if array[0] != 4 {
		t.Fatal("array dereference failed value copy")
	}
	slice := []int{1}
	aliased := MustValue(Deref(&slice, site))
	aliased[0] = 2
	if slice[0] != 2 {
		t.Fatal("slice dereference lost alias")
	}
	box := &checkedBox{N: 3}
	var reader checkedReader = box
	same := MustValue(Assert[*checkedBox](reader, Assertion{Target: "*checkedBox"}))
	same.N = 5
	if reader.Read() != 5 {
		t.Fatal("assertion lost pointer identity or method set")
	}
	var typedNil *checkedBox
	var nilReader checkedReader = typedNil
	result, ok, err := AssertOK[*checkedBox](nilReader, Assertion{})
	if err != nil || !ok || result != nil {
		t.Fatalf("typed nil: %v %v %v", result, ok, err)
	}
	empty, ok, err := AssertOK[checkedReader](nil, Assertion{})
	if err != nil || ok || empty != nil {
		t.Fatalf("nil interface: %v %v %v", empty, ok, err)
	}
	original := checkedValueBox{N: 8}
	var stored checkedReader = original
	copy := MustValue(Assert[checkedValueBox](stored, Assertion{}))
	copy.N = 10
	if stored.Read() != 8 {
		t.Fatal("assertion cloned or aliased a native value incorrectly")
	}
}
func TestCheckedValueFailures(t *testing.T) {
	site := ValueSite{Name: "i", Line: 4}
	_, err := Assert[int](nil, Assertion{Target: "int", Site: site})
	if err == nil || err.Error() != "BASHPP-EASSERT-FAIL: interface value has dynamic type , not int" {
		t.Fatalf("nil assertion: %v", err)
	}
	mismatch, ok, err := AssertOK[[2]int]("not an array", Assertion{})
	if err != nil || ok || mismatch != [2]int{} {
		t.Fatalf("comma-ok zero: %v %v %v", mismatch, ok, err)
	}
	impossible := Assertion{Source: "I", Target: "U", Impossible: true, Site: site}
	_, err = Assert[int](1, impossible)
	if err == nil || err.Error() != "BASHPP-EASSERT-IMPOSSIBLE: U cannot be asserted from I" {
		t.Fatalf("impossible: %v", err)
	}
	_, _, commaErr := AssertOK[int](1, impossible)
	if commaErr == nil || commaErr.Error() != err.Error() {
		t.Fatal("comma-ok hid impossible assertion")
	}
	for _, index := range []int{-1, 2} {
		_, err := CheckIndex(index, 2, site)
		if err == nil {
			t.Fatalf("accepted index %d", index)
		}
	}
	if _, err := CheckIndex(uint64(math.MaxUint64), 2, site); err == nil {
		t.Fatal("index overflow")
	}
	if _, err := Index([]int(nil), 0, site); err == nil {
		t.Fatal("nil slice indexed")
	}
	if _, err := MakeSlice[int](2, 1, site); err == nil || err.Error() != "BASHPP-EBUILTIN-SIZE: make slice length/capacity is invalid: 2/1" {
		t.Fatalf("slice size: %v", err)
	}
	if _, err := MakeMap[string, int](-1, site); err == nil || err.Error() != "BASHPP-EBUILTIN-SIZE: make map size must not be negative" {
		t.Fatalf("map size: %v", err)
	}
}
func TestCheckedValueUnwindAndLaziness(t *testing.T) {
	var pointer *int
	if pointer != nil && MustValue(Deref(pointer, ValueSite{})) > 0 {
		t.Fatal("nil condition")
	}
	original := &ValueError{Code: "BASHPP-ENIL-DEREF", Message: "dereference of nil pointer", Site: ValueSite{Line: 6}}
	defer func() {
		recovered := recover()
		value, ok := AsValueError(recovered)
		if !ok || value != original {
			t.Fatalf("lost typed error: %#v", recovered)
		}
		var status interface{ ExitStatus() int }
		if !errors.As(recovered.(error), &status) || status.ExitStatus() != 2 {
			t.Fatalf("lost status: %#v", recovered)
		}
	}()
	MustValue(0, original)
	t.Fatal("continued after failure")
}
