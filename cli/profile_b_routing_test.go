// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/qiangli/bash/interp"
	"github.com/qiangli/bash/syntax"
)

// profileBExecProbe stands in for every command provider below the shell
// selection boundary, including Bashy's in-process coreutils handler. Profile B
// assigns the commands in this file to the shell, so reaching this probe is a
// routing failure regardless of the eventual provider.
type profileBExecProbe struct {
	startupPosix []bool
	calls        [][]string
}

func (p *profileBExecProbe) wire(opts []interp.RunnerOption, posix bool, _ []string, in io.Reader, out, errOut io.Writer) []interp.RunnerOption {
	p.startupPosix = append(p.startupPosix, posix)
	return append(opts, interp.StdIO(in, out, errOut), interp.ExecHandlers(func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
		return func(ctx context.Context, args []string) error {
			p.calls = append(p.calls, append([]string(nil), args...))
			return fmt.Errorf("Profile B command escaped shell routing: %w", interp.NewExitStatus(125))
		}
	}))
}

func withProfileBRoutingCLI(t *testing.T, path string, stdin io.Reader) (*interp.Runner, *profileBExecProbe, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()

	// newRunner consults process-wide CLI globals. Tests using this helper are
	// intentionally not parallel; preserve every value for the rest of the
	// package suite.
	oldArgs := os.Args
	oldFlags := flag.CommandLine
	oldPosix := *posix
	oldWire := AgentOSWireExec
	t.Cleanup(func() {
		os.Args = oldArgs
		flag.CommandLine = oldFlags
		*posix = oldPosix
		AgentOSWireExec = oldWire
	})

	flag.CommandLine = flag.NewFlagSet("sh", flag.ContinueOnError)
	os.Args = []string{"sh"}
	*posix = false
	t.Setenv("PATH", path)
	t.Setenv("SHELLOPTS", "")
	t.Setenv("BASHOPTS", "")
	unsetTestEnv(t, "POSIXLY_CORRECT")
	unsetTestEnv(t, "POSIX_PEDANTIC")

	probe := new(profileBExecProbe)
	AgentOSWireExec = probe.wire
	r, err := newRunner()
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	if err := interp.StdIO(stdin, &stdout, &stderr)(r); err != nil {
		t.Fatal(err)
	}
	if err := interp.Dir(t.TempDir())(r); err != nil {
		t.Fatal(err)
	}
	if len(probe.startupPosix) != 1 || !probe.startupPosix[0] {
		t.Fatalf("AgentOS wiring observed startup POSIX modes %v, want [true]", probe.startupPosix)
	}
	return r, probe, &stdout, &stderr
}

func runProfileBRoute(t *testing.T, r *interp.Runner, src string) uint8 {
	t.Helper()
	err := run(r, strings.NewReader(src+"\n"), "profile-b-routing")
	if err == nil {
		return 0
	}
	var status interp.ExitStatus
	if errors.As(err, &status) {
		return uint8(status)
	}
	t.Fatalf("run %q: %v", src, err)
	return 255
}

func assertNoProfileBExec(t *testing.T, probe *profileBExecProbe) {
	t.Helper()
	if len(probe.calls) != 0 {
		t.Fatalf("command reached external/AgentOS execution handler: %q", probe.calls)
	}
}

func assertProfileBIntrinsicRoute(t *testing.T, invocation string) {
	t.Helper()
	r, probe, _, stderr := withProfileBRoutingCLI(t, "", nil)
	status := runProfileBRoute(t, r, invocation)
	assertNoProfileBExec(t, probe)
	if status == 127 || strings.Contains(stderr.String(), "command not found") {
		t.Fatalf("intrinsic %q was gated by PATH: status=%d stderr=%q", invocation, status, stderr.String())
	}
}

func writeProfileBPathMarker(t *testing.T, dir, name string) {
	t.Helper()
	path := filepath.Join(dir, name)
	content := []byte("#!/bin/sh\nexit 97\n")
	if runtime.GOOS == "windows" {
		path += ".exe"
		content = []byte("not executed")
	}
	if err := os.WriteFile(path, content, 0o755); err != nil {
		t.Fatal(err)
	}
}

func assertProfileBRegularBuiltinRoute(t *testing.T, name, invocation string, wantStatus uint8, checkOutput func(*testing.T, string)) {
	t.Helper()

	// In strict POSIX mode a regular builtin is not selected unless its name
	// can first be found through PATH.
	r, probe, stdout, stderr := withProfileBRoutingCLI(t, "", nil)
	if status := runProfileBRoute(t, r, invocation); status != 127 {
		t.Fatalf("%s without PATH: status=%d, want 127; stdout=%q stderr=%q", name, status, stdout.String(), stderr.String())
	}
	assertNoProfileBExec(t, probe)
	if !strings.Contains(stderr.String(), "command not found") {
		t.Fatalf("%s without PATH: stderr=%q, want command-not-found diagnostic", name, stderr.String())
	}

	// Once lookup succeeds, the shell builtin must run directly. The marker
	// exits 97 if executed, and the injected provider exits 125 if reached.
	pathDir := t.TempDir()
	writeProfileBPathMarker(t, pathDir, name)
	r, probe, stdout, stderr = withProfileBRoutingCLI(t, pathDir, nil)
	if status := runProfileBRoute(t, r, invocation); status != wantStatus {
		t.Fatalf("%s with PATH marker: status=%d, want %d; stdout=%q stderr=%q", name, status, wantStatus, stdout.String(), stderr.String())
	}
	assertNoProfileBExec(t, probe)
	if strings.Contains(stderr.String(), "command not found") {
		t.Fatalf("%s remained PATH-gated after successful lookup: %q", name, stderr.String())
	}
	if checkOutput != nil {
		checkOutput(t, stdout.String())
	}
}

func TestProfileBRouteAlias(t *testing.T)   { assertProfileBIntrinsicRoute(t, "alias") }
func TestProfileBRouteBg(t *testing.T)      { assertProfileBIntrinsicRoute(t, "bg") }
func TestProfileBRouteCd(t *testing.T)      { assertProfileBIntrinsicRoute(t, "cd .") }
func TestProfileBRouteCommand(t *testing.T) { assertProfileBIntrinsicRoute(t, "command alias") }
func TestProfileBRouteFc(t *testing.T)      { assertProfileBIntrinsicRoute(t, "fc -l") }
func TestProfileBRouteFg(t *testing.T)      { assertProfileBIntrinsicRoute(t, "fg") }
func TestProfileBRouteGetopts(t *testing.T) { assertProfileBIntrinsicRoute(t, "getopts a route_opt") }
func TestProfileBRouteHash(t *testing.T)    { assertProfileBIntrinsicRoute(t, "hash") }
func TestProfileBRouteJobs(t *testing.T)    { assertProfileBIntrinsicRoute(t, "jobs") }
func TestProfileBRouteKill(t *testing.T)    { assertProfileBIntrinsicRoute(t, "kill -l") }
func TestProfileBRouteRead(t *testing.T)    { assertProfileBIntrinsicRoute(t, "read route_value") }
func TestProfileBRouteUmask(t *testing.T)   { assertProfileBIntrinsicRoute(t, "umask") }
func TestProfileBRouteUnalias(t *testing.T) { assertProfileBIntrinsicRoute(t, "unalias -a") }
func TestProfileBRouteWait(t *testing.T)    { assertProfileBIntrinsicRoute(t, "wait") }

func TestProfileBRouteEcho(t *testing.T) {
	assertProfileBRegularBuiltinRoute(t, "echo", "echo PROFILE_B_ECHO", 0, func(t *testing.T, got string) {
		if got != "PROFILE_B_ECHO\n" {
			t.Fatalf("echo output=%q, want builtin output", got)
		}
	})
}

func TestProfileBRouteFalse(t *testing.T) {
	assertProfileBRegularBuiltinRoute(t, "false", "false", 1, nil)
}

func TestProfileBRoutePrintf(t *testing.T) {
	assertProfileBRegularBuiltinRoute(t, "printf", "printf %s PROFILE_B_PRINTF", 0, func(t *testing.T, got string) {
		if got != "PROFILE_B_PRINTF" {
			t.Fatalf("printf output=%q, want builtin output", got)
		}
	})
}

func TestProfileBRoutePwd(t *testing.T) {
	assertProfileBRegularBuiltinRoute(t, "pwd", "pwd", 0, func(t *testing.T, got string) {
		if strings.TrimSpace(got) == "" {
			t.Fatal("pwd builtin produced no directory")
		}
	})
}

func TestProfileBRouteTest(t *testing.T) {
	assertProfileBRegularBuiltinRoute(t, "test", "test x = x", 0, nil)
}

func TestProfileBRouteTrue(t *testing.T) {
	assertProfileBRegularBuiltinRoute(t, "true", "true", 0, nil)
}

func TestProfileBRouteSh(t *testing.T) {
	r, probe, _, _ := withProfileBRoutingCLI(t, "", nil)
	if got := r.LangVariant(); got != syntax.LangPOSIX {
		t.Fatalf("argv[0]=sh selected language %v, want POSIX", got)
	}
	// A readonly assignment error in a command-word prefix is fatal only on
	// the strict argv0=sh route; ':' avoids involving PATH in the probe.
	status := runProfileBRoute(t, r, "readonly route_guard=old\nroute_guard=new :\n:")
	assertNoProfileBExec(t, probe)
	if status == 0 || !r.Exited() {
		t.Fatalf("argv[0]=sh did not engage strict POSIX execution: status=%d exited=%v", status, r.Exited())
	}
}

func TestProfileBRouteTime(t *testing.T) {
	const src = "time -p :"
	file, err := syntax.NewParser(bashyParseOpts(syntax.LangPOSIX)...).Parse(strings.NewReader(src), "profile-b-routing")
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Stmts) != 1 {
		t.Fatalf("parsed %d statements, want 1", len(file.Stmts))
	}
	if _, ok := file.Stmts[0].Cmd.(*syntax.TimeClause); !ok {
		t.Fatalf("time parsed as %T, want *syntax.TimeClause", file.Stmts[0].Cmd)
	}

	r, probe, _, stderr := withProfileBRoutingCLI(t, "", nil)
	if status := runProfileBRoute(t, r, src); status != 0 {
		t.Fatalf("time reserved-word route status=%d stderr=%q", status, stderr.String())
	}
	assertNoProfileBExec(t, probe)
	for _, label := range []string{"real ", "user ", "sys "} {
		if !strings.Contains(stderr.String(), label) {
			t.Fatalf("time route did not emit %q field: %q", label, stderr.String())
		}
	}
}
