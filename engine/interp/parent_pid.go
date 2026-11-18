package interp

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// A native child shell of an in-process background job has the Go host as its
// kernel parent. This private, one-hop bridge preserves the shell-visible
// parent identity; it does not change getppid for arbitrary native programs.
const bashyParentPIDEnv = "BASHY_PARENT_PID"

func (r *Runner) startupParentPID() int {
	physical := os.Getppid()
	if _, standalone := r.sigReset.(OSSignalResetter); !standalone {
		return physical
	}
	parent, logical, ok := strings.Cut(r.Env.Get(bashyParentPIDEnv).String(), ":")
	if !ok {
		return physical
	}
	pid, err := strconv.Atoi(parent)
	if err != nil || pid <= 0 || pid != physical {
		return physical
	}
	pid, err = strconv.Atoi(logical)
	if err != nil || pid <= 0 {
		return physical
	}
	return pid
}

func (r *Runner) childParentPIDBridge(ctx context.Context, path, argv0 string) string {
	if _, standalone := r.sigReset.(OSSignalResetter); !standalone {
		return ""
	}
	bg, _ := ctx.Value(bgProcCtxKey{}).(*bgProc)
	if bg == nil || bg.carrier == nil || bg.publishPidToBang || bg.carrier.Pid() <= 0 {
		return ""
	}
	// An exec replaces this job instead of creating its child. A primary
	// external background job similarly has the host as its actual parent.
	// The one-file Bashy payload also serves utility links such as env. An
	// inode match alone would leak this private shell handoff into an applet's
	// environment, where it is visible to env(1) and scheduled jobs.
	if replacing, _ := ctx.Value(execReplacingCtxKey{}).(bool); replacing || !shellEntryName(argv0) || !sameShellExecutable(path) {
		return ""
	}
	return strconv.Itoa(os.Getpid()) + ":" + strconv.Itoa(bg.carrier.Pid())
}

func shellEntryName(argv0 string) bool {
	name := filepath.Base(argv0)
	name = strings.TrimPrefix(name, "-")
	if len(name) > 4 && strings.EqualFold(name[len(name)-4:], ".exe") {
		name = name[:len(name)-4]
	}
	switch name {
	case "sh", "bash", "bashy":
		return true
	}
	return false
}

// Only our own payload, or a launcher paired with that exact payload, can
// consume the bridge. Do not add private metadata to unrelated executables.
func sameShellExecutable(path string) bool {
	self, err := os.Executable()
	if err != nil {
		return false
	}
	return sameShellExecutableAs(path, self)
}

func sameShellExecutableAs(path, self string) bool {
	selfInfo, err := os.Stat(self)
	if err != nil {
		return false
	}
	// Stat follows installation symlinks while keeping a parent-side /proc
	// descriptor usable even when its target path exceeds PATH_MAX.
	candidate, err := os.Stat(path)
	if err != nil {
		return false
	}
	if os.SameFile(candidate, selfInfo) {
		return true
	}
	// The current payload identifies its installed sibling launcher. Merely
	// placing a .real link next to an unrelated program proves nothing about
	// that program and must not add metadata to its environment.
	if !strings.HasSuffix(self, ".real") {
		return false
	}
	launcher, err := os.Stat(strings.TrimSuffix(self, ".real"))
	return err == nil && os.SameFile(candidate, launcher)
}
