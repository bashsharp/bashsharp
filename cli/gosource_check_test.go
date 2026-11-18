// Copyright (c) 2026 qiangli
// See LICENSE for licensing information

package cli

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/front"
	_ "github.com/bashsharp/bashsharp/transpile" // wires front.ShellCheck, as cmd/bashy does
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func withCommandGiven(t *testing.T, given bool) {
	t.Helper()
	previous := commandFlagGiven
	commandFlagGiven = func() bool { return given }
	t.Cleanup(func() { commandFlagGiven = previous })
}

func runContentCheckOn(t *testing.T, body string) (int, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "p.bsh")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	oldCommand, oldStdin, oldArgs := *command, *readStdin, flag.CommandLine.Args()
	t.Cleanup(func() { *command, *readStdin = oldCommand, oldStdin; _ = flag.CommandLine.Parse(oldArgs) })
	*command, *readStdin = "", false
	if err := flag.CommandLine.Parse([]string{path}); err != nil {
		t.Fatal(err)
	}
	withGoSourceSelection(t, front.GoSourceResolution{Check: true, ContentCheck: true})
	withCommandGiven(t, false)
	var err error
	stderr := captureStderr(t, func() { err = runContentCheck() })
	if err == nil {
		return 0, stderr
	}
	return exitStatusOf(t, err), stderr
}

// TestContentCheckGoUnitLoadsWithoutEntryCalls: a Go compilation unit under a
// plain --check takes the Go front end with RunMain false and no runner.
func TestContentCheckGoUnitLoadsWithoutEntryCalls(t *testing.T) {
	var calls int
	var runMain bool
	withGoSourceHook(t, func(_ []front.GoSourceFile, opts front.GoSourceOptions) (*front.GoSourceProgram, error) {
		calls++
		runMain = opts.RunMain
		file, perr := syntax.NewParser().Parse(strings.NewReader("echo ran\n"), "p.bsh")
		if perr != nil {
			t.Fatal(perr)
		}
		return &front.GoSourceProgram{File: file, Package: "main", Main: "main"}, nil
	})
	rc, stderr := runContentCheckOn(t, "//go:build norun\n\npackage main\n\nfunc main() { for {} }\n")
	if rc != 0 || calls != 1 || runMain || stderr != "" {
		t.Fatalf("rc=%d calls=%d RunMain=%v stderr=%q", rc, calls, runMain, stderr)
	}
}

func TestContentCheckShell(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	body := "touch " + marker + "\nwhile true; do :; done\n"
	for _, tc := range []struct {
		name, src, want string
		rc              int
	}{
		{"valid program is not run", body, "", 0},
		{"semantic error", body + "func deref(p *int) int { return *p }\n", "BASHPP-ENULL-DEREF: p may be nil when dereferenced", 2},
		{"syntax error", "touch " + marker + "\nif then fi (\n", "must be followed by a statement list", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rc, stderr := runContentCheckOn(t, tc.src)
			if rc != tc.rc || !strings.Contains(stderr, tc.want) {
				t.Fatalf("rc=%d stderr=%q, want rc=%d containing %q", rc, stderr, tc.rc, tc.want)
			}
			if strings.HasPrefix(stderr, "bashy: ") {
				t.Errorf("diagnostic must be verbatim file:line:col text: %q", stderr)
			}
			if _, err := os.Stat(marker); err == nil {
				t.Fatal("--check executed the program body")
			}
		})
	}
}

func TestContentCheckDashAndEmptyCommand(t *testing.T) {
	withGoSourceSelection(t, front.GoSourceResolution{Check: true, ContentCheck: true})
	stdin := func(content string) {
		f, err := os.CreateTemp(t.TempDir(), "stdin")
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString(content)
		f.Seek(0, 0)
		old := os.Stdin
		os.Stdin = f
		t.Cleanup(func() { os.Stdin = old; f.Close() })
	}
	oldCommand, oldStdin := *command, *readStdin
	t.Cleanup(func() { *command, *readStdin = oldCommand, oldStdin; _ = flag.CommandLine.Parse(nil) })
	*command, *readStdin = "", false

	withCommandGiven(t, false)
	stdin("func d(p *int) int { return *p }\n")
	if err := flag.CommandLine.Parse([]string{"-"}); err != nil {
		t.Fatal(err)
	}
	var err error
	stderr := captureStderr(t, func() { err = runContentCheck() })
	if exitStatusOf(t, err) != 2 || !strings.Contains(stderr, "BASHPP-ENULL-DEREF") {
		t.Fatalf("dash stdin: err=%v stderr=%q", err, stderr)
	}

	// Explicit empty -c: stdin holds a bad program that must never be read.
	*command = ""
	withCommandGiven(t, true)
	stderr = captureStderr(t, func() { err = runContentCheck() })
	if err != nil || stderr != "" {
		t.Fatalf("empty -c: err=%v stderr=%q", err, stderr)
	}
}
