// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

//go:build windows

package interp

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/qiangli/bash/execbudget"
)

func TestWindowsExecBudgetStopsBeforeCreateProcess(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	self = filepath.ToSlash(self)
	self = "/" + strings.ToLower(self[:1]) + self[2:]
	file, err := syntax.NewParser().Parse(strings.NewReader("'"+self+"'\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, value string
		wantStarts  int
	}{
		{"below", strings.Repeat("x", execbudget.BashyArgMax-4096), 1},
		{"above", strings.Repeat("x", execbudget.BashyArgMax), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			r, err := New(StdIO(nil, nil, &stderr), Env(expand.ListEnviron("PATH=", "BIG="+tc.value)))
			if err != nil {
				t.Fatal(err)
			}
			starts := 0
			ctx := context.WithValue(context.Background(), execStartOverrideCtxKey{}, func(*exec.Cmd) error {
				starts++
				return errors.New("stub stop")
			})
			_ = r.Run(ctx, file)
			if starts != tc.wantStarts {
				t.Fatalf("started %d children, want %d; stderr=%q", starts, tc.wantStarts, stderr.String())
			}
			if got := strings.Contains(stderr.String(), "argument list too long"); got != (tc.wantStarts == 0) {
				t.Fatalf("stderr = %q, want budget error iff no child started", stderr.String())
			}
		})
	}
}
