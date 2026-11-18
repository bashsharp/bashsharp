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

// TestDollarAtStarAlternateFidelity covers the `:+` alternate operator on the
// `$@`/`$*` and array `[@]`/`[*]` forms, mirroring bash's dollar-at-star10.sub.
// `recho` prints one line per argument (nothing for zero arguments), so it
// distinguishes "one empty field" (`<>`) from "no field at all".
func TestDollarAtStarAlternateFidelity(t *testing.T) {
	t.Parallel()

	const recho = "recho() { for a in \"$@\"; do printf 'argv = <%s>\\n' \"$a\"; done; }\n"

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			// A single CTLNUL (0x7f) positional parameter is set and
			// non-null, so `${@:+nonnull}` / `${*:+nonnull}` fire and expand
			// to the word. Previously the parameter's own 0x7f byte leaked.
			name: "ctlnul_positional_alternate",
			in:   recho + "set -- $'\\x7f'\nrecho \"${@:+nonnull}\"\nrecho \"${*:+nonnull}\"",
			want: "argv = <nonnull>\nargv = <nonnull>\n",
		},
		{
			// An array with a single null element is set but null, so `:+`
			// does not fire: `"${arr[*]:+w}"` / `"${arr[@]:+w}"` expand to a
			// single empty field, exactly like the bare `"${arr[*]}"`.
			name: "null_array_element_alternate",
			in:   recho + "myvar[0]=\nrecho \"${myvar[*]:+nonnull}\"\nrecho \"${myvar[@]:+nonnull}\"",
			want: "argv = <>\nargv = <>\n",
		},
		{
			// A non-null array element fires the alternate.
			name: "nonnull_array_element_alternate",
			in:   recho + "myvar[0]=x\nrecho \"${myvar[*]:+nonnull}\"\nrecho \"${myvar[@]:+nonnull}\"",
			want: "argv = <nonnull>\nargv = <nonnull>\n",
		},
		{
			// With no positional parameters the alternate yields nothing.
			name: "no_positional_alternate_empty",
			in:   recho + "set --\nrecho \"${@:+nonnull}\"\nrecho \"${*:+nonnull}\"",
			want: "",
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
			r, err := New(Dir(t.TempDir()), StdIO(nil, &buf, &buf))
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
