// Copyright (c) 2026, the bashy fork
// See LICENSE for licensing information

package syntax

import (
	"strings"
	"testing"
)

// TestDbracketParseError pins the bash 5.3 wording the shared engine
// produces for malformed `[[ ]]` conditionals. The want strings are the
// exact stderr bash 5.3 emits for each script (run as `./s`).
func TestDbracketParseError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "dynamic_operator", // dbracket__020
			src:  "# c\nop='=='\n[[ a $op a ]] && echo true\n[[ a $op b ]] || echo false\n",
			want: "./s: line 3: unexpected token `$op', conditional binary operator expected\n" +
				"./s: line 3: syntax error near `$op'\n" +
				"./s: line 3: `[[ a $op a ]] && echo true'",
		},
		{
			name: "unary_redirection", // dbracket__025
			src:  "[[ -f < ]] && echo 'should be parse error'\n",
			want: "./s: line 1: unexpected argument `<' to conditional unary operator\n" +
				"./s: line 1: syntax error near `<'\n" +
				"./s: line 1: `[[ -f < ]] && echo 'should be parse error''",
		},
		{
			name: "quoted_paren_literal", // dbracket__033
			src:  "[[ '(' foo ]]\necho status=$?\n",
			want: "./s: line 1: unexpected token `foo', conditional binary operator expected\n" +
				"./s: line 1: syntax error near `foo'\n" +
				"./s: line 1: `[[ '(' foo ]]'",
		},
		{
			name: "missing_unary_arg", // dbracket__035
			src:  "[[ -z ]]\necho status=$?\n",
			want: "./s: line 1: unexpected argument `]]' to conditional unary operator\n" +
				"./s: line 1: syntax error near `]]'\n" +
				"./s: line 1: `[[ -z ]]'",
		},
		{
			name: "extra_unary_arg", // dbracket__037
			src:  "[[ -z '>' -- ]]\necho status=$?\n",
			want: "./s: line 1: syntax error in conditional expression: unexpected token `--'\n" +
				"./s: line 1: syntax error near `--'\n" +
				"./s: line 1: `[[ -z '>' -- ]]'",
		},
		{
			name: "empty", // dbracket__039
			src:  "[[ ]]\necho status=$?\n",
			want: "./s: line 1: syntax error near `]]'\n" +
				"./s: line 1: `[[ ]]'",
		},
		{
			name: "leading_and", // dbracket__040
			src:  "[[ && ]]\necho status=$?\n",
			want: "./s: line 1: unexpected token `&&' in conditional command\n" +
				"./s: line 1: syntax error near `&'\n" +
				"./s: line 1: `[[ && ]]'",
		},
		{
			name: "fd_redirection_operator", // dbracket__041
			src:  "[[ a 3< b ]]\necho status=$?\n",
			want: "./s: line 1: unexpected token `3', conditional binary operator expected\n" +
				"./s: line 1: syntax error near `3<'\n" +
				"./s: line 1: `[[ a 3< b ]]'",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := NewParser().Parse(strings.NewReader(tc.src), "./s")
			if err == nil {
				t.Fatal("expected a parse error")
			}
			got, ok := DbracketParseError(err, []byte(tc.src), "./s")
			if !ok {
				t.Fatalf("DbracketParseError did not recognise the [[ ]] error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("wrong diagnostic:\nwant:\n%s\ngot:\n%s", tc.want, got)
			}
		})
	}
}

// TestDbracketParseErrorNonConditional confirms the helper declines errors
// that are not `[[ ]]` conditionals, so callers fall back to their generic
// ParseError handling.
func TestDbracketParseErrorNonConditional(t *testing.T) {
	t.Parallel()

	_, err := NewParser().Parse(strings.NewReader("if then\n"), "./s")
	if err == nil {
		t.Fatal("expected a parse error")
	}
	if _, ok := DbracketParseError(err, []byte("if then\n"), "./s"); ok {
		t.Fatal("DbracketParseError should not claim a non-conditional error")
	}
}
