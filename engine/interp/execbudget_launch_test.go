// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

package interp

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/qiangli/bash/execbudget"
)

func TestBashPPProcessLaunchHonorsArgMax(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self)
	cmd.Env = []string{"BIG=" + strings.Repeat("x", execbudget.BashyArgMax)}
	process, err := bashPPStartCmd(context.Background(), cmd, 1)
	if process != nil || err == nil || !strings.Contains(err.Error(), "argument list too long") || cmd.Process != nil {
		t.Fatalf("over-budget process launch = process %v, err %v, os process %v", process, err, cmd.Process)
	}
}
