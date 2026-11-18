// Copyright (c) 2026, the sh authors.
// See LICENSE for licensing information.

package interp

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestBashResidueFidelity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "ifs.tests posix special export keeps inline IFS",
			in:   "x=a:b:c:d:e\nset -o posix\nIFS=: export x\necho $x",
			want: "a b c d e\n",
		},
		{
			name: "appendop1.sub integer indexed bare append",
			in:   "typeset -ia iarr=(2 2 3)\niarr+=1\ntypeset -p iarr\niarr+=(4 5 6)\ntypeset -p iarr",
			want: "declare -ai iarr=([0]=\"3\" [1]=\"2\" [2]=\"3\")\n" +
				"declare -ai iarr=([0]=\"3\" [1]=\"2\" [2]=\"3\" [3]=\"4\" [4]=\"5\" [5]=\"6\")\n",
		},
		{
			name: "arith5.sub intmax min unary minus",
			in:   "intmax_min1=$((2**63))\nintmax_min2=$((-2**63))\necho $(( -${intmax_min1} ))\necho $(( -${intmax_min2} ))",
			want: "-9223372036854775808\n-9223372036854775808\n",
		},
		{
			name: "nquote4.tests unclosed x brace escapes",
			in:   "printf '<%s>\\n' $'\\x{abcX' $'\\x{01234567X' $'\\x{bde'",
			want: "<\xbcX>\n<gX>\n<\xde>\n",
		},
		{
			name: "exportfunc.tests imported simple function",
			in:   "foo",
			want: "exportfunc ok 1\n",
		},
		{
			name: "execute_cmd last argument underscore",
			in: "printf 'initial=<%s>\\n' \"$_\"\n" +
				": a b; printf 'builtin=<%s>\\n' \"$_\"\n" +
				"f() { printf 'entry=<%s>\\n' \"$_\"; : x y; printf 'body=<%s>\\n' \"$_\"; }; f c d\n" +
				"printf 'function=<%s>\\n' \"$_\"\n" +
				"( : m n ); printf 'subshell=<%s>\\n' \"$_\"\n" +
				": p q | : r s; printf 'pipeline=<%s>\\n' \"$_\"\n" +
				"x=1; printf 'assignment=<%s>\\n' \"$_\"",
			want: "initial=<>\n" +
				"builtin=<b>\n" +
				"entry=<b>\n" +
				"body=<y>\n" +
				"function=<d>\n" +
				"subshell=<d>\n" +
				"pipeline=<d>\n" +
				"assignment=<>\n",
		},
		{
			name: "assoc16.sub single quoted subscript transform",
			in: "declare -A A\n" +
				"A[\"Darwin\"]=darjeeling\n" +
				"A['$(echo Darwin ; echo stderr>&2)']=darjeeling\n" +
				"echo ${A[$(echo Darwin ; echo stderr>&2)]@Q}\n" +
				"echo ${A['$(echo Darwin ; echo stderr>&2)']@Q}\n" +
				"echo ${A[$(echo Darwin ; echo stderr>&2)]:+set}\n" +
				"echo ${A['$(echo Darwin ; echo stderr>&2)']:+set}",
			want: "stderr\n'darjeeling'\n'darjeeling'\nstderr\nset\nset\n",
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

			var buf bytes.Buffer
			opts := []RunnerOption{Dir(t.TempDir()), StdIO(nil, &buf, &buf), WithBashCompatErrors(true)}
			if strings.Contains(tt.name, "exportfunc.tests") {
				opts = append(opts, Env(expand.ListEnviron("BASH_FUNC_foo%%=() { echo exportfunc ok 1; }")))
			} else if strings.Contains(tt.name, "last argument underscore") {
				opts = append(opts, Env(expand.ListEnviron()))
			}
			r, err := New(opts...)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := r.Run(ctx, file); err != nil {
				buf.WriteString(err.Error())
			}
			if got := buf.String(); got != tt.want {
				t.Fatalf("wrong output:\nwant: %q\ngot:  %q", tt.want, got)
			}
		})
	}
}
