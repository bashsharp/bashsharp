// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

//go:build windows

package interp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/qiangli/bash/winmode"
)

// exec7.sub puts a 0655 extensionless script before a 0755 one on PATH.
// The current owner must skip the former, then run the latter as shell text.
func TestWindowsExecPathOwnerModeAndPlainScript(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "testa")
	second := filepath.Join(root, "testb")
	for _, dir := range []string{first, second} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "foo"), []byte("echo testb\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	firstFile := filepath.Join(first, "foo")
	secondFile := filepath.Join(second, "foo")
	if err := winmode.Set(firstFile, 0o655); err != nil {
		t.Fatal(err)
	}
	if err := winmode.Set(secondFile, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = winmode.Set(firstFile, 0o755) })
	exts := []string{".com", ".exe", ".bat", ".cmd"}
	if found, err := findExecutable(first, "foo", exts); err == nil {
		t.Fatalf("0655 owner-nonexecutable path resolved as %q", found)
	}
	if found, err := findExecutable(second, "foo", exts); err != nil || found != "foo" {
		t.Fatalf("0755 path = %q, %v; want foo", found, err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path, args, missing, cleanup := preparePlatformExec(second, secondFile, secondFile, []string{"foo", "arg"})
	if path != self || len(args) != 3 || args[0] != self || args[1] != secondFile || args[2] != "arg" || missing != "" || cleanup != nil {
		t.Fatalf("plain script prepared as path=%q args=%q missing=%q has-cleanup=%t", path, args, missing, cleanup != nil)
	}
}
