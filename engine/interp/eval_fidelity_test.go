// Copyright (c) 2024, the sh authors.
// See LICENSE for licensing information.

package interp

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// TestEvalFidelity covers two bash-5.3 fidelity gaps exercised by the
// assign-extended / assign spec cases:
//
//   - eval'd code is anchored to the absolute script line where it
//     physically sits, so a runtime diagnostic reports that line rather
//     than the eval string's own line 1; a function called from within
//     eval still reports its own body line.
//   - a readonly array append or element store made through a nameref is
//     reported under the nameref name as written, while a scalar nameref
//     assignment reports the resolved target (a bash asymmetry).
//
// `want` is the combined stdout+stderr; a script whose final command
// exits non-zero appends the "exit status N" line that Runner.Run reports.
func TestEvalFidelity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input, want string
	}{
		// assign__047: eval'd `a+=(4)` / `r+=(4)` on a readonly array
		// report the eval's absolute script line (3 and 5), and the
		// nameref form names the nameref `r`, not its target `a`.
		{
			"a=(1 2 3)\nreadonly -a a\neval 'a+=(4)'\necho \"${a[@]}\"\neval 'declare -n r=a; r+=(4)'\necho \"${a[@]}\"\n",
			"./s: line 3: a: readonly variable\n1 2 3\n./s: line 5: r: readonly variable\n1 2 3\n",
		},
		// A multi-line eval body keeps each statement on its own
		// absolute script line: `x=3` is physically on line 4.
		{
			"readonly x=1\neval '\ny=2\nx=3'\n",
			"./s: line 4: x: readonly variable\nexit status 1",
		},
		// A function invoked from eval reports its own body line (3),
		// not the eval call's line (5): the offset must not leak in.
		{
			"readonly z=1\nf() {\n  z=9\n}\neval 'f'\n",
			"./s: line 3: z: readonly variable\nexit status 1",
		},
		// Scalar nameref assignment reports the resolved target (`a`),
		// but array append (`s+=(4)`) and element store (`s[0]=9`)
		// through a nameref report the nameref name (`s`).
		{
			"a=1\nreadonly a\ndeclare -n r=a\nr=5\nb=(1 2 3)\nreadonly b\ndeclare -n s=b\ns+=(4)\ns[0]=9\n",
			"./s: line 4: a: readonly variable\n./s: line 8: s: readonly variable\n./s: line 9: s: readonly variable\nexit status 1",
		},
	}

	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			t.Parallel()

			parser := syntax.NewParser(syntax.Variant(syntax.LangBash))
			file, err := parser.Parse(bytes.NewReader([]byte(tt.input)), "./s")
			if err != nil {
				t.Fatal(err)
			}

			var buf bytes.Buffer
			r, err := New(
				Dir(t.TempDir()),
				StdIO(nil, &buf, &buf),
				WithBashCompatErrors(true),
				WithBashSource([]byte(tt.input)),
			)
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
