// Copyright (c) 2017, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func runBashDiag(t *testing.T, src string) string {
	t.Helper()
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(src), "s")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	var cb lockedBuffer
	r, err := New(
		StdIO(nil, &cb, &cb),
		WithBashCompatErrors(true),
		WithBashSource([]byte(src)),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := r.Run(ctx, file); err != nil {
		cb.Write([]byte(err.Error()))
	}
	return cb.String()
}

func TestAssignArrayDiagnosticsFidelity(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "quoted_subscript_arith_error",
			in: "a[\"1\"]=2\n" +
				"echo status=$? len=${#a[@]}\n" +
				"a['2']=3\n" +
				"echo status=$? len=${#a[@]}\n" +
				"a[2 + \"3\"]=5\n" +
				"echo status=$? len=${#a[@]}\n" +
				"a[3 + '4']=5\n" +
				"echo status=$? len=${#a[@]}\n",
			want: "status=0 len=1\n" +
				"s: line 3: '2': arithmetic syntax error: operand expected (error token is \"'2'\")\n" +
				"status=1 len=1\n" +
				"status=0 len=2\n" +
				"s: line 7: 3 + '4': arithmetic syntax error: operand expected (error token is \"'4'\")\n" +
				"status=1 len=2\n",
		},
		{
			name: "negative_substring_keeps_leading_space",
			in: "a=(1 2 3 4 5)\n" +
				"echo \"${a[@]: 1: -3}\"\n" +
				"echo done=$?\n",
			want: "s: line 2:  -3: substring expression < 0\n" +
				"done=0\n",
		},
		{
			name: "negative_substring_no_space",
			in: "a=(1 2 3 4 5)\n" +
				"echo \"${a[@]:1:-3}\"\n" +
				"echo done=$?\n",
			want: "s: line 2: -3: substring expression < 0\n" +
				"done=0\n",
		},
		{
			name: "negative_substring_final_status",
			in: "a=(1 2 3 4 5)\n" +
				"echo \"${a[@]: 1: -3}\"\n",
			want: "s: line 2:  -3: substring expression < 0\n" +
				"exit status 1",
		},
		{
			name: "exp8_invalid_indexed_subscript_leaves_empty_array",
			in: "array=( [$'x\\001y\\177z']=foo )\n" +
				"declare -p array\n",
			want: "s: line 1: xy\x7fz: arithmetic syntax error: invalid arithmetic operator (error token is \"\x7fz\")\n" +
				"declare -a array=()\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := runBashDiag(t, tc.in); got != tc.want {
				t.Fatalf("wrong output in %q:\nwant: %q\ngot:  %q", tc.in, tc.want, got)
			}
		})
	}
}
