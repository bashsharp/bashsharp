// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

//go:build unix

package interp

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/qiangli/bash/execbudget"
)

func TestDetachedExecHonorsBashyArgMax(t *testing.T) {
	var stderr bytes.Buffer
	r, err := New(Env(expand.ListEnviron("PATH=/bin:/usr/bin", "BIG="+strings.Repeat("x", execbudget.BashyArgMax))), StdIO(nil, nil, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	r.Reset()
	status := runDetachedExec(context.Background(), r, "setsid", []string{"true"}, true)
	if status.code != 126 || !strings.Contains(stderr.String(), "argument list too long") {
		t.Fatalf("detached over-budget launch = status %d, stderr %q", status.code, stderr.String())
	}
}
