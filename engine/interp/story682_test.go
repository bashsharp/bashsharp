// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp

import (
	"testing"

	"github.com/qiangli/bash/pathconv"
)

// Sprint 245, story 682: platform-neutral coverage for the Windows pieces
// that survived story 678 — the logical PWD a CDPATH hit records, and the
// child-environment conversion policy. Each takes an explicit windows flag
// and mount table so the Windows logic is proven on any host.

// story682Mounts is the bash 5.3 harness layout on Windows: BASHY_ROOT is
// the run's private tree and /tmp is the run's TEMP, the two mounts
// builtins1.sub walks.
func story682Mounts() *pathconv.Mounts {
	return pathconv.NewMounts(`C:\Temp\bash53-1\root`, nil, `C:\Temp\bash53-1\tmp`)
}

func TestCdpathLogicalWindowsMode(t *testing.T) {
	t.Parallel()

	m := story682Mounts()
	tests := []struct {
		name      string
		cur       string
		base      string
		path      string
		candidate string
		want      string
	}{
		{
			// builtins1.sub: CDPATH=.:/tmp; cd bash-dir-a; pwd must print
			// /tmp/bash-dir-a, not the directory the /tmp mount points at.
			name: "mounted element keeps its spelling",
			cur:  "/c/work", base: "/tmp", path: "bash-dir-a",
			candidate: `C:\Temp\bash53-1\tmp\bash-dir-a`,
			want:      "/tmp/bash-dir-a",
		},
		{
			name: "posix element with dots is normalized",
			cur:  "/c/work", base: "/usr/../usr", path: "bin",
			candidate: `C:\Temp\bash53-1\root\usr\bin`,
			want:      "/usr/bin",
		},
		{
			name: "the dot element resolves against the logical dir",
			cur:  "/tmp", base: ".", path: "sub",
			candidate: `C:\Temp\bash53-1\tmp\sub`,
			want:      "/tmp/sub",
		},
		{
			name: "a relative element does too",
			cur:  "/usr", base: "share", path: "doc",
			candidate: `C:\Temp\bash53-1\root\usr\share\doc`,
			want:      "/usr/share/doc",
		},
		{
			name: "a native element maps back through the mounts",
			cur:  "/c/work", base: `C:\Temp\bash53-1\tmp`, path: "d",
			candidate: `C:\Temp\bash53-1\tmp\d`,
			want:      "/tmp/d",
		},
		{
			name: "an unmounted native element keeps its drive",
			cur:  "/c/work", base: `E:\data`, path: "d",
			candidate: `E:\data\d`,
			want:      "/e/data/d",
		},
		{
			name: "a natively spelled logical dir falls back to the mounts",
			cur:  `C:\work`, base: ".", path: "d",
			candidate: `C:\Temp\bash53-1\tmp\d`,
			want:      "/tmp/d",
		},
	}
	for _, tt := range tests {
		got := cdpathLogicalMode(m, tt.cur, tt.base, tt.path, tt.candidate, true)
		if got != tt.want {
			t.Errorf("%s: cdpathLogicalMode(%q, %q, %q, %q) = %q, want %q",
				tt.name, tt.cur, tt.base, tt.path, tt.candidate, got, tt.want)
		}
	}
	// Unix: the resolved path, always.
	if got := cdpathLogicalMode(nil, "/home", "/tmp", "d", "/tmp/d", false); got != "/tmp/d" {
		t.Errorf("unix = %q, want /tmp/d", got)
	}
}

// TestCdpathLogicalRecordsPWD ties the helper to what the fixture measures:
// the spelling cd echoes for a CDPATH hit is the one it records as $PWD and
// the one `pwd` prints back.
func TestCdpathLogicalRecordsPWD(t *testing.T) {
	t.Parallel()

	m := story682Mounts()
	const cur = "/c/work"
	operand := cdpathLogicalMode(m, cur, "/tmp", "bash-dir-a", `C:\Temp\bash53-1\tmp\bash-dir-a`, true)
	if operand != "/tmp/bash-dir-a" {
		t.Fatalf("cd echoes %q", operand)
	}
	apath := `C:\Temp\bash53-1\tmp\bash-dir-a`
	pwd := cdLogicalPWDMode(m, cur, operand, apath, false, true)
	if pwd != "/tmp/bash-dir-a" {
		t.Errorf("PWD = %q, want /tmp/bash-dir-a", pwd)
	}
	if got := logicalDirMode(m, pwd, apath, true); got != "/tmp/bash-dir-a" {
		t.Errorf("pwd prints %q, want /tmp/bash-dir-a", got)
	}
}
