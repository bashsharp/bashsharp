// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package expand

import (
	"reflect"
	"testing"
)

// An alternate word that produces a quoted null (`""`, `"$scalar"`,
// `"${!ref}"`) forces a field only when the substitution actually
// fires. When it does not fire — including an indirect `${!ref+word}`
// whose target variable is unset — the empty result is elided like any
// other unquoted empty, matching bash 5.3.
func TestAlternateQuotedNullFiresOnlyWhenTriggered(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		src  string
		env  []string
		want []string
	}{
		// Non-indirect: `+` does not fire when the parameter is unset,
		// even if the alternate word is a quoted null.
		{"PlusQuotedEmptyUnset", `printf '%s\n' ${y+""}`, nil, nil},
		{"PlusQuotedScalarUnset", `printf '%s\n' ${y+"${y}"}`, nil, nil},
		// `+` fires when the parameter is set (even to empty); a quoted
		// null alternate then yields one empty field.
		{"PlusQuotedEmptySet", `printf '%s\n' ${y+""}`, []string{"y="}, []string{""}},
		// Indirect: the trigger follows the target variable, not the
		// reference. hooksSlice=x with x unset → no field.
		{"IndirectPlusUnsetTarget", `printf '%s\n' ${!hooksSlice+"${!hooksSlice}"}`,
			[]string{"hooksSlice=x"}, nil},
		// x set but empty → `+` fires → one empty field.
		{"IndirectPlusEmptyTarget", `printf '%s\n' ${!hooksSlice+"${!hooksSlice}"}`,
			[]string{"hooksSlice=x", "x="}, []string{""}},
		// x set to a value → `+` fires → that value.
		{"IndirectPlusSetTarget", `printf '%s\n' ${!hooksSlice+"${!hooksSlice}"}`,
			[]string{"hooksSlice=x", "x=VAL"}, []string{"VAL"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			word := parseCallArg(t, tt.src, 2)
			got, err := Fields(&Config{Env: ListEnviron(tt.env...)}, word)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("want %#v, got %#v", tt.want, got)
			}
		})
	}
}

func TestPosixAssignSubstWordPreservesQuotedBackslash(t *testing.T) {
	t.Parallel()

	word := parseCallArg(t, `printf '%s\n' ${d=a"\\"c}`, 2)
	env := testEnv{}
	got, err := Fields(&Config{Env: env, Posix: true}, word)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{`a\c`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fields: want %#v, got %#v", want, got)
	}
	if got := env.Get("d").String(); got != `a\c` {
		t.Fatalf("assigned value: want %q, got %q", `a\c`, got)
	}
}
