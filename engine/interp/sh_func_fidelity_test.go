// Copyright (c) 2017, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// TestShFuncFidelity covers the make test-bash `sh-func` fixture:
// function-local set -u produces correct error messages with the
// right filename and line number, and the errexit status matches bash.
func TestShFuncFidelity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		// sh-func__011 — set -u inside a function; unbound variable on
		// the top-level line after the function returns should report
		// the correct file, line, and exit status.
		{
			input: "g=global\nf() {\n  set -u\n  echo \"g = $g\"\n}\nf\necho $L",
			want:  "g = global\n./s: line 7: L: unbound variable\nexit status 1",
		},
	}

	for _, tt := range tests {
		tt := tt
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
