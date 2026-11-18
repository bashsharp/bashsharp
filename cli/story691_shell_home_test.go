// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package cli

import (
	"os"
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/interp"
)

// Sprint 246, story 691 (~/.bash_logout on Windows): invocation.tests runs
// `HOME=$TDIR ${THIS_SH} --login -c 'logout'` and wants the logout file
// under $TDIR to run. The startup and logout paths asked os.UserHomeDir,
// which on Windows reads %USERPROFILE% and ignores $HOME outright, so the
// shell looked in the runner's own profile directory and printed nothing.
func TestShellHomeFollowsTheShellsHOME(t *testing.T) {
	t.Parallel()

	const want = "/tmp/invocation-4242"
	r, err := interp.New(interp.Env(expand.ListEnviron("HOME=" + want)))
	if err != nil {
		t.Fatal(err)
	}
	if got := shellHome(r); got != want {
		t.Errorf("shellHome = %q, want the shell's own HOME %q", got, want)
	}
	if hostHome, _ := os.UserHomeDir(); hostHome == want {
		t.Skip("the host home happens to equal the fixture's HOME; the test proves nothing")
	}

	// With no HOME at all the shell still falls back to the host's home,
	// so an ordinary interactive session keeps finding its ~/.bashrc.
	bare, err := interp.New(interp.Env(expand.ListEnviron()))
	if err != nil {
		t.Fatal(err)
	}
	hostHome, _ := os.UserHomeDir()
	if got := shellHome(bare); got != hostHome {
		t.Errorf("shellHome without HOME = %q, want the host home %q", got, hostHome)
	}
	if got := shellHome(nil); got != hostHome {
		t.Errorf("shellHome(nil) = %q, want the host home %q", got, hostHome)
	}
}
