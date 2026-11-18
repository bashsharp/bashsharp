// Copyright (c) 2026 qiangli
// See LICENSE for licensing information

//go:build unix

package cli

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestBashProcessCompletesBackgroundBuiltinOutput(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "background-output")
	script := "echo marker > " + strconv.Quote(outPath) + " &"
	out, exit := runBuiltBash(t, dir, script)
	if exit != 0 {
		t.Fatalf("bash exited %d, want 0; output=%q", exit, out)
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("background output was lost when bash exited: %v", err)
	}
	if string(got) != "marker\n" {
		t.Fatalf("background output = %q, want marker newline", got)
	}
}
