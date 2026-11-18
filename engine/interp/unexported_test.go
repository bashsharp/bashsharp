// Copyright (c) 2017, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestElapsedString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in    time.Duration
		posix bool
		want  string
	}{
		{time.Nanosecond, false, "0m0.000s"},
		{time.Millisecond, false, "0m0.001s"},
		{time.Millisecond, true, "0.00"},
		{2500 * time.Millisecond, false, "0m2.500s"},
		{2500 * time.Millisecond, true, "2.50"},
		{
			10*time.Minute + 10*time.Second,
			false,
			"10m10.000s",
		},
		{
			10*time.Minute + 10*time.Second,
			true,
			"610.00",
		},
		{31 * time.Second, false, "0m31.000s"},
		{102 * time.Second, false, "1m42.000s"},
	}
	for _, tc := range tests {
		t.Run(tc.in.String(), func(t *testing.T) {
			got := elapsedString(tc.in, tc.posix, ".")
			if got != tc.want {
				t.Fatalf("wanted %q, got %q", tc.want, got)
			}
		})
	}
}

func TestCarriedLocaleDecimalPoint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		locale string
		want   string
		ok     bool
	}{
		{"C", ".", true},
		{"POSIX", ".", true},
		{"de_DE.iso88591", ",", true},
		{"de_DE.ISO8859-1", ",", true},
		{"de_DE.ISO-8859-1", ",", true},
		{"de_CH.ISO-8859-1", "", false},
		{"de_DE.UTF-8", "", false},
		{"fr_FR.ISO-8859-1", "", false},
		{"de_DE", "", false},
		{"", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.locale, func(t *testing.T) {
			got, ok := carriedLocaleDecimalPoint(tc.locale)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("carriedLocaleDecimalPoint(%q) = %q, %v; want %q, %v",
					tc.locale, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestWithDecimalPointString(t *testing.T) {
	t.Parallel()
	if got, want := withDecimalPoint("1.25", "٫"), "1٫25"; got != want {
		t.Fatalf("withDecimalPoint = %q, want %q", got, want)
	}
}

func TestTrapPrintPosixNoArgs(t *testing.T) {
	t.Parallel()

	// bash --posix prints the full default-disposition list only for `trap
	// -p`; a bare `trap` lists only the non-default traps (so nothing when
	// none are set), exactly as in non-POSIX mode. The full dump is gated on
	// the -p flag, not merely on POSIX mode.
	var fullList strings.Builder
	fullList.WriteString("trap -- - EXIT\n")
	for _, sig := range sortedSignalEntries() {
		fullList.WriteString("trap -- - ")
		fullList.WriteString(sig.Name)
		fullList.WriteByte('\n')
	}
	fullList.WriteString("trap -- - DEBUG\n")
	fullList.WriteString("trap -- - ERR\n")
	fullList.WriteString("trap -- - RETURN\n")

	tests := []struct {
		cmd  string
		want string
	}{
		{"trap", ""},
		{"trap -p", fullList.String()},
	}
	for _, tc := range tests {
		t.Run(tc.cmd, func(t *testing.T) {
			t.Parallel()

			file, err := syntax.NewParser().Parse(strings.NewReader("set -o posix; "+tc.cmd), "")
			if err != nil {
				t.Fatal(err)
			}
			var stdout bytes.Buffer
			r, err := New(StdIO(nil, &stdout, nil))
			if err != nil {
				t.Fatal(err)
			}
			if err := r.Run(context.Background(), file); err != nil {
				t.Fatal(err)
			}
			if got := stdout.String(); got != tc.want {
				t.Fatalf("wrong output for %s\nwant:\n%sgot:\n%s", tc.cmd, tc.want, got)
			}
		})
	}
}

// TestTrapNumericFirstOperandResets covers the POSIX rule that a numeric first
// operand naming a valid signal makes every operand a condition to reset to
// default — `trap 2 QUIT` resets signals 2 and QUIT rather than installing "2"
// as the action for QUIT. bash applies this in both POSIX and default modes.
func TestTrapNumericFirstOperandResets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			"reset-two-signals",
			"trap 'echo trapped' INT QUIT; trap 2 QUIT; trap -p INT QUIT",
			"",
		},
		{
			"invalid-number-is-action",
			"trap 512 QUIT; trap -p QUIT",
			"trap -- '512' SIGQUIT\n",
		},
		{
			// bash prints filtered `trap -p` operands in the order given,
			// not in numeric order (TERM=15 listed before QUIT=3 here).
			"filtered-print-keeps-operand-order",
			"trap 'x' TERM QUIT; trap -p TERM QUIT",
			"trap -- 'x' SIGTERM\ntrap -- 'x' SIGQUIT\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			file, err := syntax.NewParser().Parse(strings.NewReader(tc.src), "")
			if err != nil {
				t.Fatal(err)
			}
			var stdout bytes.Buffer
			r, err := New(StdIO(nil, &stdout, nil))
			if err != nil {
				t.Fatal(err)
			}
			if err := r.Run(context.Background(), file); err != nil {
				t.Fatal(err)
			}
			if got := stdout.String(); got != tc.want {
				t.Fatalf("wrong output for %q\nwant:\n%sgot:\n%s", tc.src, tc.want, got)
			}
		})
	}
}

// TestTrapActionReEvaluated covers two POSIX trap-p requirements: the trap
// action is re-parsed and re-evaluated on every delivery (so an alias defined
// after `trap` is set still expands when the trap fires), and a single trap may
// be invoked more than once, including across a foreground subshell that
// self-signals $$ — whose trap the parent owns and fires once the subshell
// finishes.
func TestTrapActionReEvaluated(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			// The alias body changes between deliveries; each fire re-reads it.
			"alias-evaluated-each-time",
			"trap X USR1\n" +
				"alias X='echo 1'\n" +
				"kill -s USR1 $$\n" +
				"alias X='echo 2'\n" +
				"kill -s USR1 $$\n",
			"1\n2\n",
		},
		{
			// The same trap fires three times, the middle one driven from a
			// foreground subshell whose `kill -s USR1 $$` targets the parent.
			"invoked-more-than-once",
			"trap 'echo trapped' USR1\n" +
				"kill -s USR1 $$\n" +
				"(kill -s USR1 $$)\n" +
				"kill -s USR1 $$\n",
			"trapped\ntrapped\ntrapped\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			file, err := syntax.NewParser().Parse(strings.NewReader("set -o posix\n"+tc.src), "")
			if err != nil {
				t.Fatal(err)
			}
			var stdout bytes.Buffer
			r, err := New(StdIO(nil, &stdout, nil))
			if err != nil {
				t.Fatal(err)
			}
			if err := r.Run(context.Background(), file); err != nil {
				t.Fatal(err)
			}
			if got := stdout.String(); got != tc.want {
				t.Fatalf("wrong output for %q\nwant:\n%sgot:\n%s", tc.src, tc.want, got)
			}
		})
	}
}

func TestTrapPrintNoArgsNonPosixUnchanged(t *testing.T) {
	t.Parallel()

	file, err := syntax.NewParser().Parse(strings.NewReader("trap; trap -p"), "")
	if err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	r, err := New(StdIO(nil, &stdout, nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), file); err != nil {
		t.Fatal(err)
	}
	if got := stdout.String(); got != "" {
		t.Fatalf("trap no-arg output in non-posix mode:\n%s", got)
	}
}

// TestUnsupportedHints guards against drift in the unsupportedHints map.
// Every key must still be recognized by IsBuiltin (otherwise the dispatcher
// won't see the name to fall into the default arm), and invoking the name
// must produce the expected "<name>: not supported in this shell — <hint>"
// shape so agentic callers can rely on it.
func TestUnsupportedHints(t *testing.T) {
	t.Parallel()

	for name, hint := range unsupportedHints {
		t.Run(name, func(t *testing.T) {
			if !IsBuiltin(name) {
				t.Fatalf("unsupportedHints lists %q but IsBuiltin doesn't recognize it; the dispatcher will never see this name", name)
			}

			file, err := syntax.NewParser().Parse(strings.NewReader(name), "")
			if err != nil {
				t.Fatalf("parsing %q: %v", name, err)
			}
			var stderr bytes.Buffer
			r, err := New(StdIO(nil, nil, &stderr))
			if err != nil {
				t.Fatalf("interp.New: %v", err)
			}
			runErr := r.Run(context.Background(), file)
			if runErr == nil {
				t.Fatalf("running %q: expected non-nil error", name)
			}
			wantPrefix := name + ": not supported in this shell — " + hint
			if got := strings.TrimRight(stderr.String(), "\n"); got != wantPrefix {
				t.Fatalf("running %q:\nwant stderr: %q\ngot:         %q", name, wantPrefix, got)
			}
		})
	}
}
