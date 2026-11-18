// Copyright (c) 2026, the sh authors.
// See LICENSE for licensing information.

package interp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestExpandTailBuiltinFidelity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "builtin-bracket__007",
			in:   "test -n x ]\necho status=$?\ntest -n x y\necho status=$?",
			want: "./s: line 1: test: x: binary operator expected\n" +
				"status=2\n" +
				"./s: line 3: test: x: binary operator expected\n" +
				"status=2\n",
		},
		{
			name: "builtin-printf__004",
			in:   "printf -v 'a[' %s 'foo'\necho status=$?",
			want: "./s: line 1: printf: `a[': not a valid identifier\n" +
				"status=2\n",
		},
		{
			name: "quote__029",
			in:   "echo $'\\uZ' $'\\u{03bc' $'\\z'",
			want: "\\uZ \\u{03bc \\z\n",
		},
		{
			name: "ifs_default_star_join_after_unset",
			in:   "IFS=:\nset -- a b c d e\nunset IFS\necho \"$*\"",
			want: "a b c d e\n",
		},
		{
			name: "brace_char_range_backslash_empty_field",
			in:   "echo {a..A}\necho {A..a}",
			want: "a ` _ ^ ]  [ Z Y X W V U T S R Q P O N M L K J I H G F E D C B A\n" +
				"A B C D E F G H I J K L M N O P Q R S T U V W X Y Z [  ] ^ _ ` a\n",
		},
		{
			name: "brace_char_range_backtick_with_suffix",
			in:   "echo -{z..A}-\necho -{z..A..2}-",
			want: "./s: line 1: bad substitution: no closing \"`\" in `-\n" +
				"./s: line 2: bad substitution: no closing \"`\" in `-\n" +
				"exit status 1",
		},
		{
			name: "posix_cmdsub_heredoc_backquote_escaped_quotes",
			in: "printf '<%s>\\n' `printf '%s\\n' \\\"1\\\"`\n" +
				"cat <<END\n" +
				"`printf '%s\\n' \\\"1\\\"`\n" +
				"\" `printf '%s\\n' \\\"2\\\"` \"\n" +
				"END",
			want: "<\"1\">\n" +
				"1\n" +
				"\" 2 \"\n",
		},
		{
			name: "quote-p_line_continuation_arithmetic_expansion",
			in: "echo \\\n" +
				"$\\\n" +
				"\\\n" +
				"(\\\n" +
				"\\\n" +
				"(\\\n" +
				"\\\n" +
				"1\\\n" +
				"\\\n" +
				" \\\n" +
				" + \\\n" +
				" \\\n" +
				"2\\\n" +
				"\\\n" +
				")\\\n" +
				"\\\n" +
				")",
			want: "3\n",
		},
		{
			name: "spaced_cmdsub_subshell_is_not_arithmetic",
			in:   "echo $( (1 + 2) )\necho after",
			want: "./s: line 1: 1: command not found\n" +
				"\n" +
				"after\n",
		},
		{
			// builtin-vars__016: a readonly-variable assignment error
			// inside `eval` discards only the eval'd command list — bash
			// contains the DISCARD within eval, which returns status 1,
			// so the enclosing function keeps running (the `status=1`
			// echo and the later global read both execute). bash reports
			// the error at the eval-call line (line 5); with the merged
			// eval-line-offset fix our eval now anchors to that line too.
			name: "builtin-vars__016",
			in: "f() {\n\tlocal x=local\n\treadonly x\n\techo $x\n" +
				"\teval 'x=bar'\n\techo status=$?\n}\nx=global\nf\necho $x",
			want: "local\n" +
				"./s: line 5: x: readonly variable\n" +
				"status=1\n" +
				"global\n",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			parser := syntax.NewParser(syntax.Variant(syntax.LangBash))
			file, err := parser.Parse(strings.NewReader(tt.in), "./s")
			if err != nil {
				t.Fatal(err)
			}

			var buf lockedBuffer
			r, err := New(
				Dir(t.TempDir()),
				StdIO(nil, &buf, &buf),
				WithBashCompatErrors(true),
				WithBashSource([]byte(tt.in)),
			)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := r.Run(ctx, file); err != nil {
				buf.Write([]byte(err.Error()))
			}
			if got := buf.String(); got != tt.want {
				t.Fatalf("wrong output:\nwant: %q\ngot:  %q", tt.want, got)
			}
		})
	}
}
