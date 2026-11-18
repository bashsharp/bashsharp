// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

//go:build darwin

package interp

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

const darwinPipelineSIGPIPEHelper = "SH_TEST_DARWIN_PIPELINE_SIGPIPE"

func TestDarwinNestedPipelineSIGPIPEIsolation(t *testing.T) {
	if os.Getenv(darwinPipelineSIGPIPEHelper) == "1" {
		runDarwinNestedPipelineSIGPIPEHelper()
		os.Exit(0)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDarwinNestedPipelineSIGPIPEIsolation$")
	cmd.Env = []string{darwinPipelineSIGPIPEHelper + "=1"}
	for _, item := range os.Environ() {
		if strings.HasPrefix(item, "GOSH_PROG=") ||
			strings.HasPrefix(item, "GOSH_CMD=") ||
			strings.HasPrefix(item, darwinPipelineSIGPIPEHelper+"=") {
			continue
		}
		cmd.Env = append(cmd.Env, item)
	}
	var stdout, stderr bytes.Buffer // bashpp-racegate:safe-private
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("helper failed: %v\nstdout:\n%s\nstderr:\n%s", err, &stdout, &stderr)
	}
	if got, want := stdout.String(), "DEFAULT:0\nIGNORED:0,0\nSURVIVED\n"; got != want {
		t.Fatalf("stdout mismatch: got %q, want %q", got, want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr: %q", stderr.String())
	}
}

func runDarwinNestedPipelineSIGPIPEHelper() {
	const script = `
producer() {
	i=0
	while test "$i" -lt 10000; do
		printf '%080d\n' "$i"
		i=$((i + 1))
	done
}
false | (producer; false) | true
printf 'DEFAULT:%s\n' "$?"
trap '' PIPE
producer 2>/dev/null | true
printf 'IGNORED:%s,%s\n' "${PIPESTATUS[0]}" "${PIPESTATUS[1]}"
printf 'SURVIVED\n'
`
	file, err := syntax.NewParser().Parse(strings.NewReader(script), "nested-sigpipe.sh")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	runner, err := New(StdIO(nil, os.Stdout, os.Stderr))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := runner.Run(context.Background(), file); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}
