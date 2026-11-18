package shellrt

import (
	"errors"
	"fmt"
	"math"
	"testing"
)

// A named width and a named float prove the update stays in the source's own
// declared type instead of being widened on its way through this package.
type numericCounter uint8
type numericScale float32

var numericTestSite = ValueSite{File: "updates/compound.bpp", Name: "n", Line: 3, Column: 8, Offset: 42}

// numericApply is one update against a fresh target, returning the target as
// it stands afterwards so a rejection can be checked for having changed
// nothing at all.
func numericApply[T Numeric, R Numeric](initial T, rhs R, op string) (T, error) {
	value := initial
	err := NumericUpdate(&value, rhs, op, numericTestSite)
	return value, err
}

func TestNumericUpdateOrdinarySuccess(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func() (any, error)
		want string
	}{
		{"add", func() (any, error) { return numericApply(6, 2, "+=") }, "8"},
		{"subtract", func() (any, error) { return numericApply(6, 2, "-=") }, "4"},
		{"multiply", func() (any, error) { return numericApply(6, 2, "*=") }, "12"},
		{"divide", func() (any, error) { return numericApply(6, 2, "/=") }, "3"},
		{"remainder", func() (any, error) { return numericApply(7, 4, "%=") }, "3"},
		{"and", func() (any, error) { return numericApply(6, 3, "&=") }, "2"},
		{"or", func() (any, error) { return numericApply(4, 3, "|=") }, "7"},
		{"xor", func() (any, error) { return numericApply(6, 3, "^=") }, "5"},
		{"shift left", func() (any, error) { return numericApply(3, 2, "<<=") }, "12"},
		{"shift right", func() (any, error) { return numericApply(12, 2, ">>=") }, "3"},
		{"and not", func() (any, error) { return numericApply(7, 2, "&^=") }, "5"},
		{"signed negative divide", func() (any, error) { return numericApply(-7, 2, "/=") }, "-3"},
		{"signed arithmetic shift", func() (any, error) { return numericApply(-8, 1, ">>=") }, "-4"},
		{"float divide", func() (any, error) { return numericApply(1.0, 4.0, "/=") }, "0.25"},
		{"float add untyped int rhs", func() (any, error) { return numericApply(1.0, 2, "+=") }, "3"},
		{"float32 scale", func() (any, error) { return numericApply(numericScale(1), 2, "*=") }, "2"},
		{"unsigned subtract", func() (any, error) { return numericApply(uint8(3), 1, "-=") }, "2"},
		{"unsigned logical shift", func() (any, error) { return numericApply(uint8(255), 1, ">>=") }, "127"},
		{"wide unsigned", func() (any, error) { return numericApply(uint64(math.MaxUint64), 1, "-=") }, "18446744073709551614"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value, err := tc.run()
			if err != nil {
				t.Fatalf("unexpected failure: %v", err)
			}
			if got := fmt.Sprint(value); got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

// The engine wraps a successful integer result at the target's declared width
// rather than rejecting it, and a named width wraps at its own size.
func TestNumericUpdateNamedWidthWraps(t *testing.T) {
	counter, err := numericApply(numericCounter(255), 1, "+=")
	if err != nil || counter != 0 {
		t.Fatalf("counter=%v err=%v", counter, err)
	}
	if _, ok := any(counter).(numericCounter); !ok {
		t.Fatalf("update did not preserve the named type: %T", counter)
	}
	wide, err := numericApply(uint64(math.MaxUint64), 1, "+=")
	if err != nil || wide != 0 {
		t.Fatalf("wide=%v err=%v", wide, err)
	}
	signed, err := numericApply(int8(127), 1, "+=")
	if err != nil || signed != -128 {
		t.Fatalf("signed=%v err=%v", signed, err)
	}
	// The most negative value divided by -1 is a defined wrap in Go, not a
	// trap, so it must not be reported as a failure.
	extreme, err := numericApply(int64(math.MinInt64), -1, "/=")
	if err != nil || extreme != math.MinInt64 {
		t.Fatalf("extreme=%v err=%v", extreme, err)
	}
	// A shift count is not an operand of the target's width.
	shifted, err := numericApply(uint8(255), 300, "<<=")
	if err != nil || shifted != 0 {
		t.Fatalf("shifted=%v err=%v", shifted, err)
	}
}

func TestNumericUpdateRejectionsPreserveTarget(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func() (any, error)
		kept string
		want string
	}{
		{"integer division by zero", func() (any, error) { return numericApply(7, 0, "/=") }, "7", "BASHPP-EUPDATE-OP: BASHPP-EEXPR-DIVZERO: division by zero"},
		{"integer remainder by zero", func() (any, error) { return numericApply(7, 0, "%=") }, "7", "BASHPP-EUPDATE-OP: BASHPP-EEXPR-DIVZERO: division by zero"},
		{"float division by zero", func() (any, error) { return numericApply(1.0, 0, "/=") }, "1", "BASHPP-EUPDATE-OP: BASHPP-EUPDATE-NONFINITE: runtime floating-point division by zero is unsupported by the scalar carrier"},
		{"float non-finite result", func() (any, error) { return numericApply(math.MaxFloat64, 2.0, "*=") }, "1.7976931348623157e+308", "BASHPP-EUPDATE-OP: BASHPP-EUPDATE-NONFINITE: runtime floating-point result is unsupported by the scalar carrier"},
		{"float remainder", func() (any, error) { return numericApply(1.0, 2.0, "%=") }, "1", "BASHPP-EUPDATE-OP: BASHPP-EEXPR-OPERAND: unsupported binary operator %"},
		{"float bitwise", func() (any, error) { return numericApply(1.0, 2.0, "&=") }, "1", "BASHPP-EUPDATE-OP: BASHPP-EEXPR-OPERAND: unsupported binary operator &"},
		{"float shift target", func() (any, error) { return numericApply(1.0, 2, "<<=") }, "1", "BASHPP-EUPDATE-OP: BASHPP-EEXPR-SHIFT: shift requires integer operands"},
		{"negative shift count", func() (any, error) { return numericApply(1, -2, "<<=") }, "1", "BASHPP-EUPDATE-OP: BASHPP-EEXPR-SHIFT: shift count must be an unsigned integer"},
		{"fractional shift count", func() (any, error) { return numericApply(1, 2.5, ">>=") }, "1", "BASHPP-EUPDATE-OP: BASHPP-EEXPR-SHIFT: shift count must be an unsigned integer"},
		{"operand overflows width", func() (any, error) { return numericApply(uint8(1), 300, "+=") }, "1", "BASHPP-EUPDATE-OP: BASHPP-EEXPR-CONVERT: constant 300 overflows uint8"},
		{"negative operand into unsigned", func() (any, error) { return numericApply(uint8(1), -1, "+=") }, "1", "BASHPP-EUPDATE-OP: BASHPP-EEXPR-CONVERT: constant -1 overflows uint8"},
		{"operand overflows named width", func() (any, error) { return numericApply(numericCounter(1), 256, "+=") }, "1", "BASHPP-EUPDATE-OP: BASHPP-EEXPR-CONVERT: constant 256 overflows numericCounter"},
		{"operand overflows signed width", func() (any, error) { return numericApply(int8(1), 128, "+=") }, "1", "BASHPP-EUPDATE-OP: BASHPP-EEXPR-CONVERT: constant 128 overflows int8"},
		{"fractional operand into integer", func() (any, error) { return numericApply(1, 1.5, "+=") }, "1", "BASHPP-EUPDATE-OP: BASHPP-EEXPR-CONVERT: cannot convert 1.5 to int"},
		{"unbounded float operand into integer", func() (any, error) { return numericApply(1, 1e30, "+=") }, "1", "BASHPP-EUPDATE-OP: BASHPP-EEXPR-CONVERT: constant 1e+30 overflows int"},
		{"operand overflows float width", func() (any, error) { return numericApply(numericScale(1), math.MaxFloat64, "+=") }, "1", "BASHPP-EUPDATE-OP: BASHPP-EEXPR-CONVERT: constant 1.7976931348623157e+308 overflows numericScale"},
		{"unsupported operator", func() (any, error) { return numericApply(1, 2, "**=") }, "1", "BASHPP-EUPDATE-OP: unsupported compound assignment operator **="},
		{"plain assignment is not an update", func() (any, error) { return numericApply(1, 2, "=") }, "1", "BASHPP-EUPDATE-OP: unsupported compound assignment operator ="},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value, err := tc.run()
			if err == nil {
				t.Fatalf("update was accepted, target now %v", value)
			}
			if got := fmt.Sprint(value); got != tc.kept {
				t.Fatalf("target changed to %s, want %s retained", got, tc.kept)
			}
			var numeric *NumericError
			if !errors.As(err, &numeric) {
				t.Fatalf("not a numeric diagnostic: %T", err)
			}
			if got := numeric.Site; got != numericTestSite {
				t.Fatalf("site %+v, want %+v", got, numericTestSite)
			}
			if got := err.Error(); got != "updates/compound.bpp: line 3: "+tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			if status := numeric.ExitStatus(); status != 2 {
				t.Fatalf("status %d, want 2", status)
			}
		})
	}
}

// The helper never panics and never lets Go's own arithmetic trap: a rejection
// is always a returned diagnostic, which is what leaves the decision to
// continue with the caller.
func TestNumericUpdateReportsRatherThanTraps(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("checked update panicked: %v", r)
		}
	}()
	if err := NumericUpdate[int, int](nil, 1, "+=", numericTestSite); err == nil {
		t.Fatal("nil target was accepted")
	} else if err.Error() != "updates/compound.bpp: line 3: BASHPP-EUPDATE-TARGET: nil assignment target" {
		t.Fatalf("got %q", err.Error())
	}
	for _, op := range []string{"/=", "%=", "<<=", ">>="} {
		if _, err := numericApply(1, 0, op); op[0] != '<' && op[0] != '>' && err == nil {
			t.Fatalf("%s by zero was accepted", op)
		}
	}
}

// An existing ValueError consumer classifies an update failure unchanged.
func TestNumericUpdateIsAValueErrorConsumer(t *testing.T) {
	_, err := numericApply(7, 0, "/=")
	value, ok := AsValueError(err)
	if !ok {
		t.Fatalf("not recognised as a value error: %v", err)
	}
	if value.Code != "BASHPP-EUPDATE-OP" || value.Site != numericTestSite {
		t.Fatalf("unexpected value error %+v", value)
	}
	if value.Message != "BASHPP-EEXPR-DIVZERO: division by zero" {
		t.Fatalf("message %q", value.Message)
	}
	// Without a file the diagnostic carries no position prefix, matching the
	// engine's own rendering when it reports without bash-compatible errors.
	bare := &NumericError{Kind: "OP", Message: "BASHPP-EEXPR-DIVZERO: division by zero"}
	if bare.Error() != "BASHPP-EUPDATE-OP: BASHPP-EEXPR-DIVZERO: division by zero" {
		t.Fatalf("bare rendering %q", bare.Error())
	}
}
