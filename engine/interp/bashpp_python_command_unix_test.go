//go:build unix

package interp_test

import "testing"

// A worker killed by a signal with a terminating default action is a
// foreground process death: $? is 128+signal (SIGTERM → 143), as for any
// external command, and the next command starts a fresh worker.
func TestBashPPPythonCommandSignalDeath(t *testing.T) {
	dir, environ := commandProject(t)
	stdout, stderr, status := runCommandScript(t, dir, environ, `py.bump
py.sigterm; echo "sigterm=$?"
py.bump
`)
	if status != 0 || stderr != "" || stdout != "1\nsigterm=143\n1\n" {
		t.Fatalf("status=%d stderr=%q stdout=%q", status, stderr, stdout)
	}
}
