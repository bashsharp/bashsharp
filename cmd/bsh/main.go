// Copyright (c) 2026 qiangli
// See LICENSE for licensing information

// Command bsh is the certified shell binary: GNU Bash 5.3 + Bash# + the
// POSIX-required coreutils userland, in one executable. It is a small main
// over three libraries that link no Yoke package — bashsharp/cli (the shell
// and Bash# front end), bashsharp/transpile (the Bash# Go-source loader) and
// coreutils (the certified applet set in cmds/all) — so its import graph
// never reaches github.com/qiangli/yoke, bashy or ycode. It is its own Go
// module (requiring only bashsharp, bash and coreutils) and builds with
// GOWORK=off; TestBshImportBoundary and TestBshModuleRequires keep it that way.
//
// Routes, by argv[0]:
//
//	bsh             Bash# shell (Bash 5.3 superset; --no-bashsharp selects
//	                plain Bash 5.3), coreutils served in-process before PATH
//	sh, bash        shell-only drop-in: PATH resolution, no in-process applets
//	coreutils, ls…  multicall coreutils applet
//	bsh schedule    the at/batch/crontab job runner POSIX at(1) needs
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/bashsharp/bashsharp/cli"
	// The Bash# Go-source loader registers front.GoSourceLoad.
	_ "github.com/bashsharp/bashsharp/transpile"
	"github.com/qiangli/bash/interp"
	"github.com/qiangli/bash/interp/ownedexec"
	_ "github.com/qiangli/coreutils/cmds/all"
	"github.com/qiangli/coreutils/multicall"
	"github.com/qiangli/coreutils/pkg/schedule"
	"github.com/qiangli/coreutils/shell"
	"github.com/qiangli/coreutils/tool"
)

func invocationName(argv0 string) string {
	name := strings.TrimPrefix(filepath.Base(argv0), "-")
	if len(name) > 4 && strings.EqualFold(name[len(name)-4:], ".exe") {
		name = name[:len(name)-4]
	}
	return name
}

func shellInvocation(argv0 string) bool {
	switch invocationName(argv0) {
	case "sh", "bash":
		return true
	}
	return false
}

func utilityInvocation(argv0 string) bool {
	name := invocationName(argv0)
	return name == "coreutils" || (name != "bsh" && !shellInvocation(argv0) && tool.Lookup(name) != nil)
}

// wireExec serves every registered coreutils applet in-process, ahead of PATH.
func wireExec(opts []interp.RunnerOption, _ bool, _ []string, in io.Reader, out, errout io.Writer) []interp.RunnerOption {
	opts = append(opts, interp.StdIO(in, out, errout))
	opts = append(opts, interp.ServedInProcess(func(name string) bool { return tool.Lookup(name) != nil }))
	return append(opts, interp.ExecHandlers(shell.Handler()))
}

// dispatch handles the one non-shell verb: the scheduler that runs jobs the
// at/batch/crontab applets queue.
func dispatch() {
	if len(os.Args) < 2 || os.Args[1] != "schedule" {
		return
	}
	cmd := schedule.NewScheduleCmd()
	cmd.SetArgs(os.Args[2:])
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func init() {
	if shellInvocation(os.Args[0]) || utilityInvocation(os.Args[0]) {
		return
	}
	cli.AgentOSDispatch = dispatch
	cli.AgentOSWireExec = wireExec
	cli.AgentOSBashPPDefault = true
	cli.VersionProduct = "bsh"
	cli.VersionCompatibility = "GNU Bash 5.3 compatible"
	cli.SuppressedForkBuiltins = nil
}

// installInheritedSignalIgnores hands the shell the SIG_IGN dispositions it
// inherited, so the interpreter keeps them immutable (BASHY_HARD_IGNORE).
// Product builds (scripts/go-product.sh) read the runtime overlay's
// pre-handler snapshot; other Linux builds read the runtime's ELF snapshot
// through multicall.
func installInheritedSignalIgnores() {
	captured := preGoIgnoredSignals()
	if captured == "" {
		captured = strings.Join(multicall.InheritedIgnoredSignalNames(), ",")
	}
	if captured == "" {
		return
	}
	seen := map[string]bool{}
	var names []string
	for _, name := range strings.Split(os.Getenv("BASHY_HARD_IGNORE")+","+captured, ",") {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	_ = os.Setenv("BASHY_HARD_IGNORE", strings.Join(names, ","))
}

func main() {
	adoptingOwnedFrame := len(os.Args) == 2 && os.Args[1] == ownedexec.Sentinel
	if err := ownedexec.Adopt(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(126)
	}
	cli.NormalizeHostEnv()
	if adoptingOwnedFrame {
		cli.AdoptGoSourceProcessEnvironment()
	}
	if utilityInvocation(os.Args[0]) {
		multicall.Main("coreutils")
		return
	}
	if !shellInvocation(os.Args[0]) {
		cli.MaybeRunJobCarrierHelper()
	}
	installInheritedSignalIgnores()
	cli.Main()
}
