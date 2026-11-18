//go:build full

package interp

// Sprint: #281; Story: #809; Story-ID: fac7e14af4a8

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// TestS281NativeScalarReadPreservesDeclaredType keeps the numeric JSON
// carrier separate from Go assignability. A wire integer is convenient host
// int storage, while its authenticated source type decides whether it may be
// used at an int, int64, or defined-integer destination.
func TestS281NativeScalarReadPreservesDeclaredType(t *testing.T) {
	type testCase struct {
		name     string
		actual   string
		expected string
		wantErr  string
	}
	tests := []testCase{
		{name: "int", actual: "int", expected: "int"},
		{name: "int64", actual: "int64", expected: "int64"},
		{name: "int to int64", actual: "int", expected: "int64", wantErr: "cannot use int value as int64"},
		{name: "int64 to int", actual: "int64", expected: "int", wantErr: "cannot use int64 value as int"},
		{name: "named integer", actual: "time.Month", expected: "time.Month"},
		{name: "named integer to int", actual: "time.Month", expected: "int", wantErr: "cannot use time.Month value as int"},
		{name: "distinct named integers", actual: "time.Month", expected: "time.Weekday", wantErr: "cannot use time.Month value as time.Weekday"},
	}
	r := &Runner{bashPPGoSource: true}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value, meta, err := bashPPNativeReadValue(bashPPBridgeValue{Kind: "int", Type: test.actual, Text: "9"})
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := value.(int); !ok {
				t.Fatalf("wire integer materialized as %T, want int storage", value)
			}
			if meta == nil || meta.kind != "bridge-scalar" || bashPPTypeText(meta.typ) != test.actual {
				t.Fatalf("declared type metadata = %#v, want %q", meta, test.actual)
			}
			expected := &syntax.BashPPNamedType{Name: &syntax.Lit{Value: test.expected}}
			err = r.bashPPCheckTypedValue(value, meta, expected)
			if test.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want %q", err, test.wantErr)
			}
		})
	}
}
