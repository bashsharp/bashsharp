package shellrt

import (
	"errors"
	"go/constant"
	"go/token"
	"testing"
)

func TestLexicalExpressionKeepsWideOperandsAndNativeIdentity(t *testing.T) {
	p := lexicalProgram(t)
	cell := Cell[int8](p.Bindings, "tiny", "tiny", KindScalar)
	cell.Value = 1
	cell.Present = true
	writeRaw(t, p, "tiny", "128")
	operand := LexicalOperand[int8](p.Bindings, "tiny", ValueSite{Name: "tiny"})
	if got := LexicalBinary[int8](operand, constant.MakeInt64(2), token.QUO, ValueSite{}); got != 64 {
		t.Fatalf("division narrowed too early: %v", got)
	}
	if got := LexicalBinary[bool](operand, constant.MakeInt64(0), token.GTR, ValueSite{}); !got {
		t.Fatal("comparison narrowed too early")
	}
	if got := LexicalBinary[int8](operand, constant.MakeInt64(1), token.ADD, ValueSite{}); got != -127 {
		t.Fatalf("result did not narrow: %v", got)
	}
	if got := LexicalPrintAddress(p.Bindings, &cell.Value, "tiny", ValueSite{}); got != int64(128) {
		t.Fatalf("alias print narrowed: %#v", got)
	}
	writeRaw(t, p, "tiny", "bad")
	site := ValueSite{File: "alias.bpp", Name: "tiny", Line: 8, Column: 4}
	_, err := TryValue(func() int8 {
		return LexicalBinary[int8](LexicalOperandAddress(p.Bindings, &cell.Value, site), constant.MakeInt64(1), token.ADD, site)
	})
	if err == nil || err.Error() != "BASHPP-EEXPR-CONVERT: cannot convert String to int8" {
		t.Fatalf("alias error=%v", err)
	}
}
func TestLexicalExactPreservesCompilerRational(t *testing.T) {
	if got := LexicalExact("1/10").ExactString(); got != "1/10" {
		t.Fatal(got)
	}
	p := lexicalProgram(t)
	cell := Cell[float64](p.Bindings, "ratio", "ratio", KindScalar)
	cell.Value = .1
	cell.Present = true
	if err := p.Bindings.NativeScalarWritten("ratio", LexicalExact("1/10")); err != nil {
		t.Fatal(err)
	}
	value, present, err := p.Bindings.ShellValue(p.Session, "ratio")
	if err != nil || !present || value != "1/10" {
		t.Fatalf("%q/%v/%v", value, present, err)
	}
}

func TestLexicalUpdatesCommitMetadataAfterValidation(t *testing.T) {
	p := lexicalProgram(t)
	x := Cell[int8](p.Bindings, "x", "x", KindScalar)
	x.Value = 1
	x.Present = true
	writeRaw(t, p, "x", "128")
	if err := LexicalNumericUpdate(p.Bindings, &x.Value, int8(2), "/=", ValueSite{}); err != nil {
		t.Fatal(err)
	}
	if x.Value != 64 {
		t.Fatal(x.Value)
	}
	text, _, err := p.Bindings.ShellValue(p.Session, "x")
	if err != nil || text != "64" {
		t.Fatalf("%q/%v", text, err)
	}
	writeRaw(t, p, "x", "010")
	err = LexicalNumericUpdate(p.Bindings, &x.Value, int8(0), "/=", ValueSite{})
	if err == nil || err.Error() != "BASHPP-EUPDATE-OP: BASHPP-EEXPR-DIVZERO: division by zero" {
		t.Fatal(err)
	}
	text, _, _ = p.Bindings.ShellValue(p.Session, "x")
	if text != "010" || x.Value != 8 {
		t.Fatalf("failed update committed: %q/%v", text, x.Value)
	}
	y := Cell[int](p.Bindings, "y", "y", KindScalar)
	y.Value = 2
	y.Present = true
	writeRaw(t, p, "y", "02")
	if err := LexicalAssignTuple(p.Bindings, []any{&x.Value, &y.Value}, []TupleValue{TupleResult(int8(7)), TupleResult(true)}, ValueSite{}); err == nil {
		t.Fatal("bad tuple accepted")
	}
	text, _, _ = p.Bindings.ShellValue(p.Session, "x")
	if text != "010" {
		t.Fatal("failed tuple changed alias spelling")
	}
	if err := LexicalAssignTuple(p.Bindings, []any{&x.Value, &y.Value}, []TupleValue{TupleResult(int8(8)), TupleResult(2)}, ValueSite{}); err != nil {
		t.Fatal(err)
	}
	text, _, _ = p.Bindings.ShellValue(p.Session, "x")
	if text != "8" {
		t.Fatal(text)
	}
}
func TestLexicalTransferKeepsOriginalFailureAndCommitBoundary(t *testing.T) {
	p := lexicalProgram(t)
	x := Cell[int](p.Bindings, "x", "x", KindScalar)
	x.Value = 1
	x.Present = true
	writeRaw(t, p, "x", "01")
	cause := errors.New("transfer refused")
	calls := 0
	transfer := func(frame int, side string, targets []any, site ValueSite) error {
		calls++
		if frame == 0 {
			return cause
		}
		*targets[0].(*int) = 1
		return nil
	}
	if err := LexicalTransferResults(p.Bindings, 0, "side", []any{&x.Value}, ValueSite{}, transfer); err != cause {
		t.Fatal(err)
	}
	text, _, _ := p.Bindings.ShellValue(p.Session, "x")
	if text != "01" || calls != 1 {
		t.Fatal("failed transfer changed metadata")
	}
	if err := LexicalTransferResults(p.Bindings, 1, "side", []any{&x.Value}, ValueSite{}, transfer); err != nil {
		t.Fatal(err)
	}
	text, _, _ = p.Bindings.ShellValue(p.Session, "x")
	if text != "1" || calls != 2 {
		t.Fatal("successful transfer did not invalidate once")
	}
}

func TestLexicalAddressKeepsStorageAndRejectsInvalidReads(t *testing.T) {
	p := lexicalProgram(t)
	x := Cell[int](p.Bindings, "x", "x", KindScalar)
	x.Value = 1
	x.Present = true
	writeRaw(t, p, "x", "010")
	address, err := LexicalAddress(p.Bindings, &x.Value, ValueSite{Name: "x"})
	if err != nil || address != &x.Value || *address != 8 {
		t.Fatalf("address=%p target=%p value=%v error=%v", address, &x.Value, x.Value, err)
	}
	*address = 9
	if err := p.Bindings.NativeWrittenAt(address); err != nil {
		t.Fatal(err)
	}
	if x.Value != 9 {
		t.Fatal("receiver copied storage")
	}
	writeRaw(t, p, "x", "bad")
	address, err = LexicalAddress(p.Bindings, &x.Value, ValueSite{Name: "x", Line: 3})
	if address != nil || err == nil || err.Error() != "BASHPP-EEXPR-CONVERT: cannot convert String to int" {
		t.Fatalf("invalid receiver %p %v", address, err)
	}
	text, _, err := p.Bindings.ShellValue(p.Session, "x")
	if err != nil || text != "bad" {
		t.Fatalf("invalid check mutated spelling: %q %v", text, err)
	}
}
