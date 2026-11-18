// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp

import "testing"

// TestNativeExecEnvKeepsShellSpelling pins the story 682 policy: a child is
// handed the values the script set, byte for byte. Only PATH — which the
// exec lookup walks — and the BASHYENV opt-ins are converted.
func TestNativeExecEnvKeepsShellSpelling(t *testing.T) {
	t.Parallel()

	m := story682Mounts()
	// varenv.tests: HOME=/a/b/c /bin/echo $HOME prints /a/b/c, although
	// /a/b/c is a real directory under the virtual root.
	env := []string{
		"HOME=/a/b/c",
		"TMPDIR=/tmp",
		"GOPATH=/c/go",
		"SYSTEMROOT=/c/Windows",
	}
	got := nativeExecEnvMountsMode(m, append([]string(nil), env...), true)
	for i := range env {
		if got[i] != env[i] {
			t.Errorf("%q was converted to %q", env[i], got[i])
		}
	}
	// TMP and TEMP are the host's own variables — os.TempDir reads them and
	// the /tmp mount is built from that, so a child shell handed the shell's
	// spelling loses its temp directory. They convert; TMPDIR does not.
	// USERPROFILE is the host's home directory variable in the same way.
	env = []string{"TMP=/tmp", "TEMP=/c/Users/x/AppData/Local/Temp", "TMPDIR=/tmp", "USERPROFILE=/c/Users/me"}
	want2 := []string{`TMP=C:\Temp\bash53-1\tmp`, `TEMP=C:\Users\x\AppData\Local\Temp`, "TMPDIR=/tmp", `USERPROFILE=C:\Users\me`}
	got = nativeExecEnvMountsMode(m, append([]string(nil), env...), true)
	for i := range want2 {
		if got[i] != want2[i] {
			t.Errorf("host temp env: got %q, want %q", got[i], want2[i])
		}
	}

	// The other exceptions still apply.
	env = []string{"BASHYENV=GOPATH/p", "GOPATH=/c/go", "PATH=/bin:/c/Go/bin"}
	want := []string{"BASHYENV=GOPATH/p", `GOPATH=C:\go`, `PATH=C:\Temp\bash53-1\root\usr\bin;C:\Go\bin`}
	got = nativeExecEnvMountsMode(m, append([]string(nil), env...), true)
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
