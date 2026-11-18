// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp_test

import (
	"context"
	"runtime"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestRunnerBash53RedirectCluster(t *testing.T) {
	// Not t.Parallel: these cases assert on process-global fd NUMBERS (3/7/8)
	// and open real descriptors via `exec N>file`. In the goroutine-subshell
	// model all simulated processes share one OS fd table, so a concurrent
	// test's transient exec fd can appear at an asserted number and flake this
	// suite (seen only under CI's 2-core `-race`). Run it in the sequential
	// phase where no parallel test is active.

	tests := []struct {
		name         string
		in           string
		want         string
		inheritedFds []int
	}{
		{
			name: "dpl_in_and_dpl_out_equivalent_for_output_fd",
			in:   "echo one 1>&2\necho two 1<&2",
			want: "one\ntwo\n",
		},
		{
			name: "bare_less_amp_fd_duplication",
			in:   "echo foo51 > lessamp.txt\nexec 6< lessamp.txt\nread line <&6\necho \"[$line]\"",
			want: "[foo51]\n",
		},
		{
			name: "bare_redirection_resets_status",
			in:   "( exit 42 )\necho status=$?\n2>&1\necho status=$?",
			want: "status=42\nstatus=0\n",
		},
		{
			name: "two_ampersand_greater_parses_as_background",
			in:   "2&>1\necho status=$?",
			want: "status=127\n",
		},
		{
			name: "explicit_dpl_out_non_fd_word_opens_file",
			in:   "echo one 1>&redir-file\necho status=$?\ncat redir-file",
			want: "status=0\none\n",
		},
		{
			name: "fd_close_with_heredoc_restores_fd",
			in:   "exec 3> fd.txt\necho hello 3>&- << EOF\nEOF\necho world >&3\nexec 3>&-\ncat fd.txt",
			want: "hello\nworld\n",
		},
		{
			name: "closed_fd_self_dup_is_noop",
			in:   ": 3>&3\necho hello",
			want: "hello\n",
		},
		{
			name: "closed_fd_self_move_is_noop",
			in:   ": 3>&3-\necho hello",
			want: "hello\n",
		},
		{
			name: "write_fd_three_repeatedly",
			in:   "exec 3> fd3.txt\necho hello >&3\necho world >&3\nexec 3>&-\ncat fd3.txt",
			want: "hello\nworld\n",
		},
		{
			name: "write_fd_four_repeatedly",
			in:   "exec 4> fd4.txt\necho hello >&4\necho world >&4\nexec 4>&-\ncat fd4.txt",
			want: "hello\nworld\n",
		},
		{
			name: "exec_open_descriptor",
			in:   "exec 3>&1\necho hi 1>&3",
			want: "hi\n",
		},
		{
			name: "exec_open_multiple_descriptors",
			in:   "exec 3>&1\nexec 4>&1\necho three 1>&3\necho four 1>&4",
			want: "three\nfour\n",
		},
		{
			name: "heredoc_to_numbered_fd",
			in:   "read a 3<<EOF <&3\nabc\nEOF\necho [$a]",
			want: "[abc]\n",
		},
		{
			// fd 7 is NOT among the shell's tracked descriptors, so `1>&7`
			// must report EBADF — even if the process happens to inherit an
			// ambient OS fd 7 (as GitHub's macOS runners do). Register no
			// inherited fds here so the verdict comes from the shell's own fd
			// table (inheritedFd() short-circuits an un-registered fd before it
			// ever probes the real descriptor); with the default [7] the test
			// would instead F_GETFL the ambient fd and spuriously succeed. This
			// is bash-faithful: bash exposes only the descriptors it opened or
			// inherited-and-tracked, not whatever is coincidentally open.
			name:         "bad_fd_seven_with_imported_ambient_fd",
			in:           "echo hi 1>&7",
			inheritedFds: []int{},
			want:         "7: Bad file descriptor\nexit status 1",
		},
		{
			name: "dpl_out_to_unopened_fd_is_bad_fd",
			in:   "echo hi >&3",
			want: "3: Bad file descriptor\nexit status 1",
		},
		{
			name: "dpl_out_to_closed_fd_is_bad_fd",
			in:   "exec 4>&-\necho hi >&4",
			want: "4: Bad file descriptor\nexit status 1",
		},
		{
			name:         "dpl_in_from_shell_closed_inherited_fd_three_is_bad_fd",
			in:           "exec 3<&-\n<&3",
			want:         "3: Bad file descriptor\nexit status 1",
			inheritedFds: []int{3, 7},
		},
		{
			name: "coproc_fds_do_not_poison_closed_fd_table",
			in:   "coproc { echo FOO; }\nread x <&${COPROC[0]}\necho $x\nexec {fd}<&${COPROC[0]}-\nexec {fd}>&${COPROC[1]}-\necho ${COPROC[0]} ${COPROC[1]}",
			want: "FOO\n-1 -1\n",
		},
		{
			name: "closed_fd_mark_is_cleared_on_reopen",
			in:   "exec 4>&-\nexec 4>&1\necho FOO >&4",
			want: "FOO\n",
		},
		{
			name: "noclobber_allows_dev_null",
			in:   "set -o noclobber\nprintf ok >/dev/null\necho status=$?",
			want: "status=0\n",
		},
		{
			name: "noclobber_clobber_operator_still_overwrites",
			in:   "echo XX >| c.txt\nset -o noclobber\necho YY > c.txt\necho status=$?\ncat c.txt\necho ZZ >| c.txt\ncat c.txt",
			want: "c.txt: cannot overwrite existing file\nstatus=1\nXX\nZZ\n",
		},
		{
			name: "scoped_fd_move_restores_closed_source",
			in:   "exec 6>&- 7>&-\nexec 7> f.txt\n: 6>&7 7>&-\necho hello >&7\n: 6>&7-\necho world >&7\nexec 7>&-\ncat f.txt",
			want: "7: Bad file descriptor\nhello\n",
		},
		{
			name: "xtrace_preserves_command_order",
			in:   "set -x\nprintf aaaa > /dev/null 2> test_osh\nset +x\ncat test_osh",
			want: "+ printf aaaa\n+ set +x\n",
		},
		{
			name: "posix_redirections_apply_left_to_right",
			in:   "set -o posix\nsh -c 'printf err >&2' 2>&1 >/dev/null",
			want: "err",
		},
		{
			name: "posix_redirection_only_expansion_is_subshell_scoped",
			in:   "set -o posix\n: > foo\n< ${x=foo}\nprintf '<%s> status=%s\\n' \"$x\" \"$?\"",
			want: "<> status=0\n",
		},
		{
			name: "posix_inout_stdout_writes_file",
			in:   "set -o posix\n: > f\nprintf abc 1<>f\ncat f",
			want: "abc",
		},
		{
			name: "posix_inout_stdin_can_write_then_read_same_fd",
			in:   "set -o posix\nprintf abc > f\nexec 0<>f\nprintf X >&0\nread -r -N 2 v <&0\nprintf '<%s>\\n' \"$v\"",
			want: "<bc>\n",
		},
		{
			name: "posix_closed_input_cat_fails",
			in:   "set -o posix\nread v <&-\nprintf 'status=%s\\n' \"$?\"",
			want: "status=1\n",
		},
		{
			name: "posix_closed_output_cat_fails",
			in:   "set -o posix\nprintf x > f\ncat f >&-\nprintf 'status=%s\\n' \"$?\"",
			want: "status=1\n",
		},
		{
			name: "posix_redirection_operand_quote_removal",
			in:   "set -o posix\n: > 'quoted file'\ncat < 'quoted file'",
			want: "",
		},
		{
			// Redirections take effect left-to-right: `1>&-` closes fd 1,
			// so the following `2>&1` dups an already-closed fd and must
			// report EBADF (bash 5.3), not silently succeed.
			name: "closed_output_fd_dup_in_order_is_bad_fd",
			in:   "echo - 1>/dev/null 3>&1 2>&3 3>&- ; { 1>&- 2>&1 ; }",
			want: "1: Bad file descriptor\nexit status 1",
		},
		{
			name: "posix_input_dup_from_write_only_fd_fails",
			in:   "set -o posix\nexec 8>f\ncat <&8\nprintf 'status=%s\\n' \"$?\"",
			want: "8: Bad file descriptor\nstatus=1\n",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			// Serial within the suite too — see the note on the parent: fd
			// numbers are process-global, so subtests must not overlap.

			file, err := syntax.NewParser().Parse(strings.NewReader(tt.in), "")
			if err != nil {
				t.Fatal(err)
			}

			var cb concBuffer
			r, err := interp.New(
				interp.Dir(t.TempDir()),
				interp.StdIO(nil, &cb, &cb),
				interp.ExecHandlers(testExecHandler),
				interp.WithInheritedFds(redirectTestInheritedFds(tt.inheritedFds)),
			)
			if err != nil {
				t.Fatal(err)
			}
			// Reclaim descriptors opened by earlier subtests' runners before
			// asserting on specific fd numbers. interp closes redirect/exec
			// fds via the *os.File finalizer (no deterministic close API — see
			// Runner.Reset), so a prior subtest's fd can still occupy e.g. fd 7
			// until GC runs. -race delays finalization enough to flake the
			// bad-fd assertions; force a collection so the fd table is clean.
			runtime.GC()
			runtime.GC()
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

// redirectTestInheritedFds returns the fds a redirect fidelity case should
// inherit as open. Cases default to fd 7 (the historical fixture); a case may
// override to simulate other inherited-open descriptors — e.g. fd 3, which the
// Go runtime holds open on Linux, so `exec 3<&-` then `<&3` must still error.
func redirectTestInheritedFds(fds []int) []int {
	if fds == nil {
		return []int{7}
	}
	return fds
}

// TestRunnerBash53RedirectErrorLine checks that a failed-open redirect on a
// compound command is reported at the line of the redirect operator (bash 5.3),
// not the first line of the statement. A `while … done < missing` redirect sits
// on the `done` line, which bash uses for the "No such file or directory" error.
func TestRunnerBash53RedirectErrorLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			// The redirect is on line 4 (`done < no-such`); bash points
			// the error there, not at the `while` on line 2.
			name: "while_compound_redirect_uses_operator_line",
			in:   "set -o errexit\nwhile read line; do\n echo $line\ndone < no-such-file.txt\necho after",
			want: "./s: line 4: no-such-file.txt: No such file or directory\nexit status 1",
		},
		{
			// A for loop behaves the same: the redirect on line 4 is the
			// reported line, not the `for` header on line 2.
			name: "for_compound_redirect_uses_operator_line",
			in:   "set -e\nfor x in 1 2; do\n echo $x\ndone < no-such-file.txt",
			want: "./s: line 4: no-such-file.txt: No such file or directory\nexit status 1",
		},
		{
			// A simple command keeps reporting on its own (single) line.
			name: "simple_command_redirect_line",
			in:   "set -e\ncat < no-such-file.txt",
			want: "./s: line 2: no-such-file.txt: No such file or directory\nexit status 1",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			file, err := syntax.NewParser().Parse(strings.NewReader(tt.in), "./s")
			if err != nil {
				t.Fatal(err)
			}

			var cb concBuffer
			r, err := interp.New(
				interp.Dir(t.TempDir()),
				interp.StdIO(nil, &cb, &cb),
				interp.WithBashCompatErrors(true),
				interp.WithBashSource([]byte(tt.in)),
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
