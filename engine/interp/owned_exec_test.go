package interp

import "testing"

func TestOwnedExecutablePathsSurviveReset(t *testing.T) {
	r, err := New(OwnedExecutablePaths("/trusted/bash", "/trusted/yoke"))
	if err != nil {
		t.Fatal(err)
	}
	r.Reset()
	if len(r.ownedExecPaths) != 2 || r.ownedExecPaths[0] != "/trusted/bash" || r.ownedExecPaths[1] != "/trusted/yoke" {
		t.Fatalf("owned executable identities lost on Reset: %q", r.ownedExecPaths)
	}
	child := r.subshell(false)
	if len(child.ownedExecPaths) != 2 || child.ownedExecPaths[0] != "/trusted/bash" || child.ownedExecPaths[1] != "/trusted/yoke" {
		t.Fatalf("owned executable identities lost in subshell: %q", child.ownedExecPaths)
	}
}
