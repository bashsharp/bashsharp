// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

//go:build unix

package interp

import (
	"context"
	"os"
	"os/exec"
	"testing"
)

func TestTimeIssue7JobControlWait4CPU(t *testing.T) {
	cmd := exec.Command(os.Getenv("GOSH_PROG"))
	cmd.Env = append(os.Environ(), "GOSH_CMD=burn_cpu")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), bgProcCtxKey{}, &bgProc{jobControl: true})
	err, user, sys := waitExecCmd(ctx, cmd)
	if err != nil {
		t.Fatalf("Wait4 child failed: %v", err)
	}
	if user+sys <= 0 {
		t.Fatalf("Wait4 discarded child CPU: user=%v sys=%v", user, sys)
	}
}
