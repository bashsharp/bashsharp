// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

//go:build windows

package interp

import (
	"context"
	"os"
	"os/exec"
	"syscall"
)

const (
	windowsDetachedProcess   = 0x00000008
	windowsCreateNewProcGrp  = 0x00000200
	windowsDetachedProcFlags = windowsDetachedProcess | windowsCreateNewProcGrp
)

// runSetsid implements the `setsid` builtin on Windows as a detached process
// launch. Windows has no POSIX session IDs or controlling terminals, so the
// closest process primitive is DETACHED_PROCESS plus CREATE_NEW_PROCESS_GROUP.
func (r *Runner) runSetsid(ctx context.Context, args []string) exitStatus {
	var exit exitStatus
parseFlags:
	for len(args) > 0 {
		switch args[0] {
		case "-f", "-w", "-c":
			args = args[1:]
		case "--":
			args = args[1:]
			break parseFlags
		default:
			if len(args[0]) > 0 && args[0][0] == '-' {
				r.errf("setsid: invalid option: %q\n", args[0])
				exit.code = 2
				return exit
			}
			break parseFlags
		}
	}
	if len(args) == 0 {
		r.errf("setsid: usage: setsid [-f] [-w] [-c] <program> [args...]\n")
		exit.code = 2
		return exit
	}
	return runDetachedExec(ctx, r, "setsid", args, true /*foreground*/)
}

func detachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: windowsDetachedProcFlags,
	}
}

func detachedExecCmd(r *Runner, path string, args []string) exec.Cmd {
	// The lookup reports the spelling without a PATHEXT suffix (see
	// findExecutable); CreateProcess needs the file.
	execPath := execFileWithExt(shellPathToOS(r.Dir, path), pathExts(r.writeEnv), func(p string) bool {
		_, err := os.Stat(p)
		return err == nil
	})
	execDir := shellPathToOS(r.Dir, r.Dir)
	return exec.Cmd{
		Path:        execPath,
		Args:        args,
		Env:         nativeExecEnv(execEnv(r.bashPPEnv())),
		Dir:         execDir,
		Stdin:       r.stdin,
		Stdout:      r.bashPPWriter(r.stdout),
		Stderr:      r.bashPPWriter(r.stderr),
		SysProcAttr: detachedSysProcAttr(),
	}
}

func runDetachedExec(ctx context.Context, r *Runner, label string, args []string, foreground bool) exitStatus {
	var exit exitStatus
	path, err := LookPathDir(r.Dir, r.writeEnv, args[0])
	if err != nil {
		r.errf("%s: %v\n", label, err)
		exit.code = 127
		return exit
	}
	cmd := detachedExecCmd(r, path, args)
	if err := startExecCmdWithinBudget(&cmd); err != nil {
		r.errf("%s: %v\n", label, err)
		exit.code = 126
		return exit
	}
	publishBgPid(ctx, cmd.Process.Pid)

	if !foreground {
		return exit
	}

	if err := cmd.Wait(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exit.code = uint8(ee.ExitCode())
			return exit
		}
		r.errf("%s: %v\n", label, err)
		exit.code = 1
	}
	return exit
}
