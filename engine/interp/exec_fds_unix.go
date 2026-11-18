// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

// Every platform but Windows can pass numbered descriptors to a child
// through exec.Cmd.ExtraFiles; plan9 shares this shape with Unix.

//go:build !windows

package interp

// prepareChildFds pads the runner's fds 3..max into ExtraFiles and names
// the live ones in BASHY_INHERITED_FDS, exactly as execExtraFiles always
// has. execPath is unused: the descriptors reach any child.
func prepareChildFds(r *Runner, execPath string) (*childFds, error) {
	extra, inherited, cleanup, err := r.execExtraFiles()
	if err != nil {
		return nil, err
	}
	c := &childFds{extraFiles: extra, cleanup: cleanup}
	if inherited != "" {
		c.env = []string{BashyInheritedFdsEnv + "=" + inherited}
	}
	return c, nil
}

// prepareSelfReexecFds is the ENOEXEC re-exec's view: the descriptors were
// already prepared for the first attempt and reach our own binary the same
// way, so the previous set is reused as is.
func prepareSelfReexecFds(r *Runner, prev *childFds) (*childFds, error) {
	return prev, nil
}

// adoptInheritedHandles is Windows-only: elsewhere inherited descriptors
// are announced through WithInheritedFds and BASHY_INHERITED_FDS.
func (r *Runner) adoptInheritedHandles(spec string) {}
