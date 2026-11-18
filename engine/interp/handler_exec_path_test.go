// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

package interp

import "testing"

func TestAnchorExecPathPreservesParentComponents(t *testing.T) {
	t.Parallel()

	if got, want := anchorExecPath("/proc/self/fd/9", "../program"), "/proc/self/fd/9/../program"; got != want {
		t.Fatalf("anchored path = %q, want %q", got, want)
	}
	if got, want := anchorExecPath("/proc/self/fd/9/", "./program"), "/proc/self/fd/9/./program"; got != want {
		t.Fatalf("anchored path with trailing slash = %q, want %q", got, want)
	}
	if got, want := anchorExecPath("/proc/self/fd/9", "/usr/bin/program"), "/usr/bin/program"; got != want {
		t.Fatalf("absolute path = %q, want %q", got, want)
	}
}

func TestLookupStatPathPreservesRetainedCwdParentComponents(t *testing.T) {
	t.Parallel()

	if got, want := lookupStatPath("/proc/self/fd/9", "../program"), "/proc/self/fd/9/../program"; got != want {
		t.Fatalf("lookup path = %q, want %q", got, want)
	}
	if got, want := lookupStatPath("/ordinary/working", "../program"), "/ordinary/program"; got != want {
		t.Fatalf("ordinary lookup path = %q, want %q", got, want)
	}
}
