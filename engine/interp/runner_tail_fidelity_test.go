// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp_test

import (
	"context"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// TestRunnerBash53RunnerTail covers residue-fidelity cases for command
// "tails": redirects, pipeline status, and arithmetic commands matched
// against bash 5.3 behaviour.
func TestRunnerBash53RunnerTail(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			// A redirect on a `(( ))` command must be active before the
			// arithmetic (and any command substitution inside it) runs, so
			// the substitution's stderr is captured by `2>`. Bash:
			//   (( a = $(stdout_stderr.py 42) + 10 )) 2>err
			// sends the inner command's stderr to err. We used to defer the
			// redirect to a late path that ArithmCmd never drained, dropping
			// it entirely and letting the stderr escape.
			name: "dparen_redirect_captures_cmdsubst_stderr",
			in:   "(( a = $(echo boom >&2; echo 5) + 10 )) 2>err.txt\necho a=$a\necho --\ncat err.txt",
			want: "a=15\n--\nboom\n",
		},
		{
			// Same shape with a redirect whose word itself comes from a
			// command substitution: the file is created and captures output.
			name: "dparen_redirect_word_from_cmdsubst",
			in:   "(( a = $(echo 7) + 1 )) > $(echo out.txt)\necho a=$a\ncat out.txt",
			want: "a=8\n",
		},
		{
			// Initial PIPESTATUS is the empty array, so expanding it yields
			// nothing — bash prints "pipestatus" with no trailing status.
			name: "initial_pipestatus_is_empty",
			in:   "echo pipestatus ${PIPESTATUS[@]}",
			want: "pipestatus\n",
		},
		{
			// A command-list compound ({ }, if, for, …) must not overwrite
			// PIPESTATUS with its own single exit status: it delegates to
			// whichever inner command ran last. Here the inner pipeline sets
			// the two-element array and the brace group leaves it intact.
			name: "brace_group_keeps_inner_pipestatus",
			in:   "{ true | false; }\necho ${PIPESTATUS[@]}",
			want: "0 1\n",
		},
		{
			// Same for an `if` whose condition is a pipeline.
			name: "if_condition_pipeline_keeps_pipestatus",
			in:   "if true | false; then :; fi\necho ${PIPESTATUS[@]}",
			want: "0 1\n",
		},
		{
			// A simple command after a pipeline collapses PIPESTATUS to its
			// own single status.
			name: "simple_command_resets_pipestatus",
			in:   "false | true\ntrue\necho ${PIPESTATUS[@]}",
			want: "0\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			file, err := syntax.NewParser().Parse(strings.NewReader(tt.in), "")
			if err != nil {
				t.Fatal(err)
			}

			var cb concBuffer
			r, err := interp.New(
				interp.Dir(t.TempDir()),
				interp.StdIO(nil, &cb, &cb),
				interp.ExecHandlers(testExecHandler),
			)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
			defer cancel()
			if err := r.Run(ctx, file); err != nil {
				cb.WriteString(err.Error())
			}

			if got := cb.String(); got != tt.want {
				t.Fatalf("wrong output in %q:\nwant: %q\ngot:  %q", tt.in, tt.want, got)
			}
		})
	}
}
