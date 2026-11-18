// Copyright (c) 2026, the bashy fork
// See LICENSE for licensing information

package interp

import (
	"regexp"
	"testing"
)

// TestBashRegexErrMsg covers case 005 of the [[ ]] fidelity work: the
// `=~` operator must report an invalid regular expression using bash
// 5.3's POSIX regcomp wording, not the raw Go regexp engine error.
//
// Real bash 5.3 prints, for `[[ foo.py =~ * ]]`:
//
//	[[: invalid regular expression `*': Repetition not preceded by valid expression
//
// bashRegexErrMsg produces the part after "[[: ".
func TestBashRegexErrMsg(t *testing.T) {
	tests := []struct {
		pat  string // raw operand bash echoes back in `...`
		want string
	}{
		{"*", "invalid regular expression `*': Repetition not preceded by valid expression"},
		{"+", "invalid regular expression `+': Repetition not preceded by valid expression"},
		{"[", "invalid regular expression `[': Unmatched [, [^, [:, [., or [="},
		{"(", "invalid regular expression `(': Unmatched ( or \\("},
		{`\`, "invalid regular expression `\\': Trailing backslash"},
	}
	for _, tc := range tests {
		t.Run(tc.pat, func(t *testing.T) {
			_, err := regexp.Compile(tc.pat)
			if err == nil {
				t.Fatalf("expected %q to fail to compile", tc.pat)
			}
			got := bashRegexErrMsg(err, tc.pat)
			if got != tc.want {
				t.Errorf("bashRegexErrMsg(%q):\n got: %q\nwant: %q", tc.pat, got, tc.want)
			}
		})
	}
}
