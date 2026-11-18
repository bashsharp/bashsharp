// Copyright (c) 2026, the outset authors
// See LICENSE for licensing information

//go:build unix

package interp_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// testCarrier462 is a minimal carrier for testing.
type testCarrier462 struct{}

type testCarrierProc462 struct {
	cmd   *exec.Cmd
	stdin *os.File
}

func (c testCarrier462) StartCarrier(ctx context.Context) (interp.CarrierProcess, error) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &testCarrierProc462{cmd: cmd}, nil
}

func (p *testCarrierProc462) Pid() int { return p.cmd.Process.Pid }
func (p *testCarrierProc462) Wait() int {
	err := p.cmd.Wait()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if exitErr.ProcessState != nil {
				if ws, ok := exitErr.ProcessState.Sys().(interface{ Signal() int }); ok {
					return ws.Signal()
				}
			}
		}
	}
	return 0
}
func (p *testCarrierProc462) Terminate() {
	_ = p.cmd.Process.Kill()
}

// TestTP462DollarBangMultipleJobs verifies that $! correctly tracks the PID
// of each background job when multiple asynchronous jobs are started.
func TestTP462DollarBangMultipleJobs(t *testing.T) {
	src := `sleep 30 &
pid1=$!
sleep 30 &
pid2=$!
echo "pid1=$pid1 pid2=$pid2"
[ "$pid1" != "$pid2" ] && echo "distinct"
[ -n "$pid1" ] && [ -n "$pid2" ] && echo "nonempty"`
	file, err := syntax.NewParser().Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	var buf concBuffer
	r, err := interp.New(
		interp.WithJobCarrier(testCarrier462{}),
		interp.StdIO(nil, &buf, &buf),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := r.Run(ctx, file); err != nil {
		t.Logf("run error: %v", err)
	}
	out := buf.String()
	fmt.Printf("TP462 output: %q\n", out)
	if !strings.Contains(out, "distinct") {
		t.Errorf("pid1 and pid2 are the same\noutput: %q", out)
	}
	if !strings.Contains(out, "nonempty") {
		t.Errorf("pid1 or pid2 is empty\noutput: %q", out)
	}
}

// TestTP462DollarBangMatchesExternalPID covers the timing-sensitive part of
// TP462: expanding $! immediately after starting a simple external command
// must return that command's PID, not the job carrier's PID.
func TestTP462DollarBangMatchesExternalPID(t *testing.T) {
	dir := t.TempDir()
	pidFile := dir + "/pid"
	src := fmt.Sprintf(`sh -c 'echo $$ > %q' &
background_pid=$!
wait "$background_pid"
read reported_pid < %q
printf 'background=%%s reported=%%s\n' "$background_pid" "$reported_pid"
[ "$background_pid" = "$reported_pid" ]`, pidFile, pidFile)
	file, err := syntax.NewParser().Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	var buf concBuffer
	r, err := interp.New(
		interp.WithJobCarrier(testCarrier462{}),
		interp.StdIO(nil, &buf, &buf),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := r.Run(ctx, file); err != nil {
		t.Fatalf("run: %v\noutput: %q", err, buf.String())
	}
}

// TestTP462CommandPrefixDollarBangMatchesExternalPID verifies that the
// transparent command builtin does not publish its in-process/carrier PID
// before the external child has started. Nested command wrappers must retain
// the same behavior.
func TestTP462CommandPrefixDollarBangMatchesExternalPID(t *testing.T) {
	for _, prefix := range []string{"command ", "command command "} {
		t.Run(strings.TrimSpace(prefix), func(t *testing.T) {
			dir := t.TempDir()
			pidFile := dir + "/pid"
			src := fmt.Sprintf(`%ssh -c 'echo $$ > %q' &
background_pid=$!
wait "$background_pid"
read reported_pid < %q
[ "$background_pid" = "$reported_pid" ]`, prefix, pidFile, pidFile)
			file, err := syntax.NewParser().Parse(strings.NewReader(src), "")
			if err != nil {
				t.Fatal(err)
			}
			var buf concBuffer
			r, err := interp.New(
				interp.WithJobCarrier(testCarrier462{}),
				interp.StdIO(nil, &buf, &buf),
			)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := r.Run(ctx, file); err != nil {
				t.Fatalf("run: %v\noutput: %q", err, buf.String())
			}
		})
	}
}

func TestTP462CommandQueryReleasesDollarBang(t *testing.T) {
	file, err := syntax.NewParser().Parse(strings.NewReader(
		"command -v sh >/dev/null & wait \"$!\"",
	), "")
	if err != nil {
		t.Fatal(err)
	}
	var buf concBuffer
	r, err := interp.New(
		interp.WithJobCarrier(testCarrier462{}),
		interp.StdIO(nil, &buf, &buf),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := r.Run(ctx, file); err != nil {
		t.Fatalf("run: %v\noutput: %q", err, buf.String())
	}
}

// TestTP462DollarBangWaitMultipleJobs verifies that wait works correctly
// for multiple background jobs with distinct PIDs.
func TestTP462DollarBangWaitMultipleJobs(t *testing.T) {
	src := `true &
pid1=$!
true &
pid2=$!
wait $pid1
echo "rc1=$?"
wait $pid2
echo "rc2=$?"`
	file, err := syntax.NewParser().Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	var buf concBuffer
	r, err := interp.New(
		interp.StdIO(nil, &buf, &buf),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := r.Run(ctx, file); err != nil {
		t.Logf("run error: %v", err)
	}
	out := buf.String()
	fmt.Printf("TP462 wait output: %q\n", out)
	if !strings.Contains(out, "rc1=0") {
		t.Errorf("wait $pid1 failed\noutput: %q", out)
	}
	if !strings.Contains(out, "rc2=0") {
		t.Errorf("wait $pid2 failed\noutput: %q", out)
	}
}

// TestTP462DollarBangAsyncLists tests the POSIX "asynchronous list" form
// where & applies to a compound command. $! should be available and correct.
func TestTP462DollarBangAsyncLists(t *testing.T) {
	src := `{ sleep 30; } &
pid1=$!
{ sleep 30; } &
pid2=$!
echo "pid1=$pid1 pid2=$pid2"
if [ "$pid1" = "$pid2" ]; then
  echo "SAME_PID"
else
  echo "DISTINCT_PIDS"
fi`
	file, err := syntax.NewParser().Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	var buf concBuffer
	r, err := interp.New(
		interp.WithJobCarrier(testCarrier462{}),
		interp.StdIO(nil, &buf, &buf),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := r.Run(ctx, file); err != nil {
		t.Logf("run error: %v", err)
	}
	out := buf.String()
	fmt.Printf("TP462 async lists output: %q\n", out)
	if strings.Contains(out, "SAME_PID") {
		t.Errorf("compound command bg jobs got same PID\noutput: %q", out)
	}
	if !strings.Contains(out, "DISTINCT_PIDS") {
		t.Errorf("compound command bg jobs did not get distinct PIDs\noutput: %q", out)
	}
}

// TestTP462DollarBangPipeline tests $! after a backgrounded pipeline.
func TestTP462DollarBangPipeline(t *testing.T) {
	src := `sleep 30 | cat &
pid1=$!
sleep 30 | cat &
pid2=$!
echo "pid1=$pid1 pid2=$pid2"
if [ "$pid1" = "$pid2" ]; then
  echo "SAME_PID"
else
  echo "DISTINCT_PIDS"
fi`
	file, err := syntax.NewParser().Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	var buf concBuffer
	r, err := interp.New(
		interp.WithJobCarrier(testCarrier462{}),
		interp.StdIO(nil, &buf, &buf),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := r.Run(ctx, file); err != nil {
		t.Logf("run error: %v", err)
	}
	out := buf.String()
	fmt.Printf("TP462 pipeline output: %q\n", out)
	if strings.Contains(out, "SAME_PID") {
		t.Errorf("pipeline bg jobs got same PID\noutput: %q", out)
	}
}
