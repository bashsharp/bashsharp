// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

//go:build windows

package interp

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func countExecAliases(t *testing.T) int {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(os.TempDir(), "bashy-exec-*.exe"))
	if err != nil {
		t.Fatal(err)
	}
	return len(matches)
}

// posixexp.tests copies the shell to $TMPDIR/sh and runs it: an
// extensionless PE image, which os/exec refuses by name. A copy of this
// binary runs as this binary (argv[0] untouched, descriptors handed
// over); any other image runs through a temporary .exe alias.
func TestWindowsExtensionlessPEExec(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	selfImage, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	where, err := os.ReadFile(`C:\Windows\System32\where.exe`)
	if err != nil {
		t.Skip("no where.exe")
	}
	tmp := t.TempDir()
	copied := filepath.Join(tmp, "sh")
	if err := os.WriteFile(copied, selfImage, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "w"), where, 0o755); err != nil {
		t.Fatal(err)
	}
	msys := "/" + strings.ToLower(filepath.ToSlash(copied)[:1]) + filepath.ToSlash(copied)[2:]

	// What os/exec is handed, with the start stubbed out.
	var calls []*exec.Cmd
	stop := errors.New("stub stop")
	start := func(cmd *exec.Cmd) error {
		clone := *cmd
		clone.Args = append([]string(nil), cmd.Args...)
		calls = append(calls, &clone)
		return stop
	}
	ctx := context.WithValue(context.Background(), execStartOverrideCtxKey{}, start)
	before := countExecAliases(t)
	file, err := syntax.NewParser().Parse(strings.NewReader("'"+msys+"' 'echo hi'; ./w /Q where.exe"), "")
	if err != nil {
		t.Fatal(err)
	}
	r, err := New(Dir(tmp))
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Run(ctx, file)
	if len(calls) != 2 {
		t.Fatalf("got %d starts, want 2", len(calls))
	}
	if calls[0].Path != self || calls[0].Args[0] != msys {
		t.Fatalf("copied shell: Path=%q Args=%q; want self with argv[0] %q", calls[0].Path, calls[0].Args, msys)
	}
	alias := calls[1].Path
	if filepath.Dir(alias) != filepath.Clean(os.TempDir()) || !strings.HasPrefix(filepath.Base(alias), "bashy-exec-") || !strings.HasSuffix(alias, ".exe") {
		t.Fatalf("other image: Path=%q, want <TEMP>\\bashy-exec-*.exe", alias)
	}
	if calls[1].Args[0] != "./w" {
		t.Fatalf("other image: Args=%q, want argv[0] ./w", calls[1].Args)
	}
	if _, err := os.Stat(alias); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("alias %q not removed after the command: %v", alias, err)
	}
	if after := countExecAliases(t); after != before {
		t.Fatalf("aliases left in TEMP: %d, was %d", after, before)
	}

	// The real thing: the copy runs as a child shell (with GOSH_PROG set,
	// this binary runs its first argument as a script), receives the open
	// descriptor a bashy child gets, and the alias runs where.exe.
	out, errb := runWindowsFdScript(t, tmp, strings.Join([]string{
		"'" + msys + "' 'echo hi'",
		"exec 7>out.txt",
		"'" + msys + "' 'echo via7 >&7'",
		"exec 7>&-",
		"./w /Q where.exe; echo rc=$?",
	}, "\n"))
	if errb != "" {
		t.Fatalf("stderr = %q", errb)
	}
	if !strings.HasPrefix(out, "hi\n") || !strings.Contains(out, "rc=0\n") {
		t.Fatalf("stdout = %q", out)
	}
	if got, err := os.ReadFile(filepath.Join(tmp, "out.txt")); err != nil || string(got) != "via7\n" {
		t.Fatalf("out.txt = %q, %v; want via7 through the handoff", got, err)
	}
	if after := countExecAliases(t); after != before {
		t.Fatalf("aliases left in TEMP: %d, was %d", after, before)
	}
}
