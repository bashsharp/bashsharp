// Copyright (c) 2017, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// lockedBuffer is a goroutine-safe buffer for capturing a runner's
// combined stdout+stderr.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestDbracketFidelity covers bash 5.3 [[ ]] conditional-command
// fidelity gaps fixed in interp/test.go. Each case runs a script
// through a Runner and compares the combined stdout+stderr (with a
// trailing "exit status N" appended when Run returns a non-nil error,
// matching interp_test.go's TestRunnerRun convention).
func TestDbracketFidelity(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		// dbracket__043.sh — tilde expansion of [[ ]] operands,
		// including a $HOME that is set but empty (which bash expands
		// to "", unlike a tilde left intact for an unset HOME).
		{
			name: "043_tilde_operands",
			in: "HOME=/root\n" +
				"[[ ~ ]]\n" +
				"echo status=$?\n" +
				"HOME=''\n" +
				"[[ ~ ]]\n" +
				"echo status=$?\n" +
				"[[ -n ~ ]]\n" +
				"echo unary=$?\n" +
				"[[ ~ == ~ ]]\n" +
				"echo status=$?\n" +
				"[[ $HOME == ~ ]]\n" +
				"echo fnmatch=$?\n" +
				"[[ ~ == $HOME ]]\n" +
				"echo fnmatch=$?\n",
			want: "status=0\nstatus=1\nunary=1\nstatus=0\nfnmatch=0\nfnmatch=0\n",
		},
		// dbracket__044.sh — a tilde-expanded =~ operand is matched
		// literally; the home directory's regex metacharacters (e.g. a
		// HOME of `^a$`) are quoted, while a $HOME on the rhs keeps its
		// regex metacharacters active.
		{
			name: "044_tilde_regex",
			in: "HOME=foo\n" +
				"[[ ~ =~ $HOME ]]\n" +
				"echo regex=$?\n" +
				"[[ $HOME =~ ~ ]]\n" +
				"echo regex=$?\n" +
				"HOME='^a$'\n" +
				"[[ ~ =~ $HOME ]]\n" +
				"echo regex=$?\n" +
				"[[ $HOME =~ ~ ]]\n" +
				"echo regex=$?\n",
			want: "regex=0\nregex=0\nregex=1\nregex=0\n",
		},
		// dbracket__029.sh — quoted numeric operands are still
		// arithmetic values in [[ ]] comparisons; the quote marks
		// themselves are not part of the arithmetic expression.
		{
			name: "029_quoted_arithmetic_operands",
			in: "[[ \"3\" -eq \"3\" ]] && echo true\n" +
				"[[ '3' -eq '3' ]] && echo true\n",
			want: "true\ntrue\n",
		},
		// dbracket__048.sh — regex compile error inside `||` does
		// not leak exit status 2 when the fallback arm succeeds.
		// bash still prints the regex error to stderr, but the
		// overall [[ ]] compound test is true and exits 0.
		{
			name: "048_regex_error_or_true",
			in: "[[ foo =~ * || -n x ]]\n" +
				"echo status=$?\n",
			want: "[[: invalid regular expression `*': Repetition not preceded by valid expression\n" +
				"status=0\n",
		},
		// dbracket__047.sh — quoted parentheses on the rhs are
		// literals even after extglob is enabled; only the unquoted
		// leading `*` remains pattern syntax.
		{
			name: "047_quoted_parens_with_extglob",
			in: "if [[ 'foo()' == *\\(\\) ]]; then echo match1; fi\n" +
				"if [[ 'foo()' == *'()' ]]; then echo match2; fi\n" +
				"if [[ 'foo()' == '*()' ]]; then echo match3; fi\n" +
				"shopt -s extglob\n" +
				"if [[ 'foo()' == *\\(\\) ]]; then echo match1; fi\n" +
				"if [[ 'foo()' == *'()' ]]; then echo match2; fi\n" +
				"if [[ 'foo()' == '*()' ]]; then echo match3; fi\n",
			want: "match1\nmatch2\nmatch1\nmatch2\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			file, err := syntax.NewParser().Parse(strings.NewReader(tc.in), "")
			if err != nil {
				t.Fatalf("parse error: %v", err)
			}

			var cb lockedBuffer
			r, err := New(StdIO(nil, &cb, &cb))
			if err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := r.Run(ctx, file); err != nil {
				cb.Write([]byte(err.Error()))
			}

			if got := cb.String(); got != tc.want {
				t.Fatalf("wrong output in %q:\nwant: %q\ngot:  %q", tc.in, tc.want, got)
			}
		})
	}
}

func TestDbracketRedirectWithCommandSubstitution(t *testing.T) {
	t.Parallel()

	src := "[[ $(stdout_stderr.py) == STDOUT ]] 2>x.txt\necho $?\n"
	file, err := syntax.NewParser().Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	tdir := t.TempDir()
	var cb lockedBuffer
	r, err := New(
		Dir(tdir),
		StdIO(nil, &cb, &cb),
		ExecHandlers(func(next ExecHandlerFunc) ExecHandlerFunc {
			return func(ctx context.Context, args []string) error {
				if len(args) > 0 && args[0] == "stdout_stderr.py" {
					hc := HandlerCtx(ctx)
					fmt.Fprintln(hc.Stdout, "STDOUT")
					fmt.Fprintln(hc.Stderr, "STDERR")
					return nil
				}
				return next(ctx, args)
			}
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := r.Run(ctx, file); err != nil {
		cb.Write([]byte(err.Error()))
	}

	if got, want := cb.String(), "0\n"; got != want {
		t.Fatalf("wrong output:\nwant: %q\ngot:  %q", want, got)
	}
	bs, err := os.ReadFile(filepath.Join(tdir, "x.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(bs), "STDERR\n"; got != want {
		t.Fatalf("wrong redirected stderr:\nwant: %q\ngot:  %q", want, got)
	}
}

func TestDbracketBashCompatDiagnostics(t *testing.T) {
	t.Parallel()

	src := "[[ foo.py =~ * ]]\n"
	file, err := syntax.NewParser().Parse(strings.NewReader(src), "./s")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	var cb lockedBuffer
	r, err := New(StdIO(nil, &cb, &cb), WithBashCompatErrors(true), WithBashSource([]byte(src)))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := r.Run(ctx, file); err != nil {
		cb.Write([]byte(err.Error()))
	}

	want := "./s: line 1: [[: invalid regular expression `*': Repetition not preceded by valid expression\nexit status 2"
	if got := cb.String(); got != want {
		t.Fatalf("wrong output:\nwant: %q\ngot:  %q", want, got)
	}
}

func TestDbracketRegexErrorOrTrue(t *testing.T) {
	t.Parallel()

	src := "[[ foo =~ * || -n x ]]\necho status=$?\n"
	file, err := syntax.NewParser().Parse(strings.NewReader(src), "./s")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	var cb lockedBuffer
	r, err := New(StdIO(nil, &cb, &cb), WithBashCompatErrors(true), WithBashSource([]byte(src)))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	runErr := r.Run(ctx, file)
	if runErr != nil {
		cb.Write([]byte(runErr.Error()))
	}

	want := "./s: line 1: [[: invalid regular expression `*': Repetition not preceded by valid expression\nstatus=0\n"
	if got := cb.String(); got != want {
		t.Fatalf("wrong output:\nwant: %q\ngot:  %q", want, got)
	}
	if runErr != nil {
		t.Fatalf("expected nil error (exit 0), got: %v", runErr)
	}
}

func TestDbracketArithErrorOrStatus(t *testing.T) {
	t.Parallel()

	src := "[[ 1 -eq + || -n x ]]\n" +
		"echo status=$?\n" +
		"[[ 0 -eq 1 || 2 -eq + ]]\n" +
		"echo status=$?\n"
	file, err := syntax.NewParser().Parse(strings.NewReader(src), "./s")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	var cb lockedBuffer
	r, err := New(StdIO(nil, &cb, &cb), WithBashCompatErrors(true), WithBashSource([]byte(src)))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := r.Run(ctx, file); err != nil {
		cb.Write([]byte(err.Error()))
	}

	want := "./s: line 1: [[: +: arithmetic syntax error: operand expected (error token is \"+\")\n" +
		"status=0\n" +
		"./s: line 3: [[: +: arithmetic syntax error: operand expected (error token is \"+\")\n" +
		"status=1\n"
	if got := cb.String(); got != want {
		t.Fatalf("wrong output:\nwant: %q\ngot:  %q", want, got)
	}
}
