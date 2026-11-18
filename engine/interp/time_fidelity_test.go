// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp

import (
	"bytes"
	"context"
	"io"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func runTimeScript(t *testing.T, src string) (stdout, stderr string) {
	t.Helper()
	return runTimeScriptWithInput(t, src, nil)
}

func runTimeScriptWithInput(t *testing.T, src string, stdin io.Reader) (stdout, stderr string) {
	t.Helper()
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(src), "s")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	var outBuf, errBuf bytes.Buffer
	r, err := New(
		StdIO(stdin, &outBuf, &errBuf),
	)
	if err != nil {
		t.Fatal(err)
	}
	// The `time` tests deliberately burn CPU so the report is non-zero, and
	// the race detector makes the interpreter roughly an order of magnitude
	// slower. At a flat 10s a loop sized to be measurable natively times out
	// under -race on a shared runner, and the deadline error lands in the
	// captured stderr — so the test fails on FORMAT, blaming the shell for the
	// harness's budget. Scale the deadline with the build instead.
	timeout := 10 * time.Second
	if raceEnabled {
		timeout = 90 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := r.Run(ctx, file); err != nil {
		errBuf.WriteString(err.Error())
	}
	return outBuf.String(), errBuf.String()
}

func maskTiming(s string) string {
	re := regexp.MustCompile(`\d+m\d+\.\d+s|\d+\.\d+`)
	return re.ReplaceAllString(s, "NUM")
}

func TestTimeFormatNullFidelity(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		src        string
		wantStdout string
		wantStderr string
	}{
		{
			name:       "null_timeformat_suppresses_timing",
			src:        "TIMEFORMAT=\"\"; time echo hello\n",
			wantStdout: "hello\n",
			wantStderr: "",
		},
		{
			name:       "null_timeformat_with_posix_flag_retains_posix_output",
			src:        "TIMEFORMAT=\"\"; time -p echo hello\n",
			wantStdout: "hello\n",
			wantStderr: "real NUM\nuser NUM\nsys NUM\n",
		},
		{
			name:       "custom_timeformat",
			src:        "TIMEFORMAT=\"elapsed: %R\"; time echo hello\n",
			wantStdout: "hello\n",
			wantStderr: "elapsed: NUM\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotOut, gotErr := runTimeScript(t, tc.src)
			if gotOut != tc.wantStdout {
				t.Errorf("stdout mismatch:\nwant: %q\ngot:  %q", tc.wantStdout, gotOut)
			}
			if maskTiming(gotErr) != tc.wantStderr {
				t.Errorf("stderr mismatch:\nwant: %q\ngot:  %q", tc.wantStderr, maskTiming(gotErr))
			}
		})
	}
}
