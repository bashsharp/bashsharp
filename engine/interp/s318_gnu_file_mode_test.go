// Copyright (c) 2026, the sh authors.
// See LICENSE for licensing information.

package interp

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Sprint 318: GNU Bash 5.3 script-file behavior the 86-fixture gate pins
// (arith, errors, nameref, quotearray, dbg-support, comsub-posix). Sprint
// 275 matched `bash -c` one-liners, where discarding the rest of the input
// line looks like an exit; a script file resumes at its next line. Every
// want below is GNU Bash 5.3.15 running the input as ./s with 2>&1.
func TestS318GNUScriptFileMode(t *testing.T) {
	tests := []struct{ input, want string }{
		{"for f in _; do continue 42 abcde; done\necho after $?\n",
			"./s: line 1: continue: too many arguments\nafter 2\n"},
		{"x=4+\ndeclare -i x\nx+=7 y=4; echo same\necho x = $x y = $y\n",
			"./s: line 3: 4+: arithmetic syntax error: operand expected (error token is \"+\")\nx = 4+ y =\n"},
		{"declare -i i\ni=0#4\necho after\n",
			"./s: line 2: 0#4: invalid number (error token is \"0#4\")\nafter\n"},
		{"declare -n r; ((r=0)); echo same $?\necho next\n",
			"./s: line 1: ((: `0': not a valid identifier\nsame 1\nnext\n"},
		{"A='3 + 5'\necho $(( 4 ? : $A ))\necho $((4 ? : $A))\necho $(( 2**-1 ))\necho after\n",
			"./s: line 2: 4 ? : 3 + 5 : expression expected (error token is \": 3 + 5 \")\n" +
				"./s: line 3: 4 ? : 3 + 5: expression expected (error token is \": 3 + 5\")\n" +
				"./s: line 4: 2**-1 : exponent less than 0 (error token is \"1 \")\nafter\n"},
		{"declare -A assoc\nkey='x],b[1'\nassoc[$key]=42\n[[ -v assoc[$key] ]]\necho $?\n",
			"0\n"},
		{"array=(1 2 3)\nBASH_COMPAT=51\nunset array[@]\ndeclare -p array\n",
			"./s: line 4: declare: array: not found\nexit status 1"},
	}
	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(bytes.NewReader([]byte(tt.input)), "./s")
			if err != nil {
				t.Fatal(err)
			}
			var buf bytes.Buffer
			r, err := New(Dir(t.TempDir()), StdIO(nil, &buf, &buf),
				WithBashCompatErrors(true), WithBashSource([]byte(tt.input)))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := r.Run(ctx, file); err != nil {
				buf.WriteString(err.Error())
			}
			if got := buf.String(); got != tt.want {
				t.Fatalf("wrong output in %q:\nwant: %q\ngot:  %q", tt.input, tt.want, got)
			}
		})
	}
}

// glibc's stdout printf fails an overflowing %s precision on Linux, but
// `printf -v` formats through Bash's own buffer (printf7.sub line 88).
func TestS318PrintfVarOverflowingStringPrecision(t *testing.T) {
	src := "TOOBIG=9223372036854775825\nprintf -v VAR \"%.${TOOBIG}s\" XY\necho \"[$VAR]\"\n"
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(bytes.NewReader([]byte(src)), "./s")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	r, err := New(Dir(t.TempDir()), StdIO(nil, &buf, &buf), WithBashCompatErrors(true), WithBashSource([]byte(src)))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), file); err != nil {
		buf.WriteString(err.Error())
	}
	if got := buf.String(); got != "[XY]\n" {
		t.Fatalf("got %q, want %q", got, "[XY]\n")
	}
}
