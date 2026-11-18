// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package interp

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Pinned source: golang/go commit 862c888e612ac346c7c4d99c9392bdfd265f33b0
// (go1.27.1), src/os/exec/exec_test.go: cmdPipeTest, TestPipes, TestContext.
// License: BSD-3-Clause, https://github.com/golang/go/blob/862c888e612ac346c7c4d99c9392bdfd265f33b0/LICENSE
// cmdPipeTest's protocol implementation and TestPipes's exact three input
// strings are retained. Adapter changes: launch this test executable instead
// of the upstream helper registry; receive stdout from Lines and inspect
// separately drained stderr after Wait. No host shell is needed, including on
// Windows. Cancellation tests keep the pipe open until cancellation so the
// child cannot accidentally complete before the operation being tested.
func TestBashPPProcessUpstreamHelper(t *testing.T) {
	if os.Getenv("BASHPP_PROCESS_PIPE_HELPER") != "1" {
		return
	}
	bufr := bufio.NewReader(os.Stdin)
	for {
		line, _, err := bufr.ReadLine()
		if err == io.EOF {
			break
		} else if err != nil {
			os.Exit(1)
		}
		if bytes.HasPrefix(line, []byte("O:")) {
			os.Stdout.Write(line)
			os.Stdout.Write([]byte{'\n'})
		} else if bytes.HasPrefix(line, []byte("E:")) {
			os.Stderr.Write(line)
			os.Stderr.Write([]byte{'\n'})
		} else {
			os.Exit(1)
		}
	}
	os.Exit(0)
}

func processPipeHelper(t *testing.T) *exec.Cmd {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestBashPPProcessUpstreamHelper$")
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GOSH_PROG=") && !strings.HasPrefix(entry, "GOSH_CMD=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "BASHPP_PROCESS_PIPE_HELPER=1")
	return cmd
}

func TestBashPPProcessUpstreamPipes(t *testing.T) {
	cmd := processPipeHelper(t)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p, err := StartLineCommand(ctx, cmd, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	for _, line := range []string{"O:I am output\n", "E:I am error\n", "O:I am output2\n"} {
		if _, err := io.WriteString(stdin, line); err != nil {
			t.Fatal(err)
		}
		if line[0] == 'O' {
			if got := <-p.Lines(); got != line[:len(line)-1] {
				t.Fatalf("got %q, want %q", got, line[:len(line)-1])
			}
		}
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if status, err := p.Wait(); status != 0 || err != nil {
		t.Fatalf("Wait = %d, %v", status, err)
	}
	if got := p.Stderr(); got != "E:I am error\n" {
		t.Fatalf("stderr = %q", got)
	}
}

func TestBashPPProcessUpstreamContext(t *testing.T) {
	cmd := processPipeHelper(t)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p, err := StartLineCommand(ctx, cmd, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if _, err := io.WriteString(stdin, "O:ready\n"); err != nil {
		t.Fatal(err)
	}
	if got := <-p.Lines(); got != "O:ready" {
		t.Fatalf("readiness = %q", got)
	}
	cancel()
	done := make(chan error, 1)
	go func() { _, err := p.Wait(); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Wait = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancel did not terminate child")
	}
	if cmd.ProcessState == nil {
		t.Fatal("child was not reaped")
	}
}
