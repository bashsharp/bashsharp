package interp

import (
	"os/exec"
	"testing"
)

func TestBashPPInjectedGoSupported(t *testing.T) {
	for goVersion, want := range map[string]bool{
		"go1.27.0": true,
		"go1.27.1": true,
		"go1.27.2": true,
		"go1.28":   true,
		"go1.26.5": false,
		"go1.24.0": false,
		"devel":    false,
		"":         false,
	} {
		if bashPPInjectedGoSupported(goVersion) != want {
			t.Errorf("bashPPInjectedGoSupported(%q) != %v", goVersion, want)
		}
	}
}

// An injected toolchain is probed for what it is: identity comes from the
// binary's own go env, not from the bootstrap review list.
func TestBashPPInjectedGoIdentity(t *testing.T) {
	hostGo, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go on PATH")
	}
	identity, err := bashPPInjectedGoIdentity(hostGo)
	if err != nil {
		t.Skipf("host go unusable for injection: %v", err)
	}
	if identity.Binary == "" || identity.Root == "" || identity.SHA256 == "" {
		t.Fatalf("incomplete identity %+v", identity)
	}
	if !bashPPInjectedGoSupported(identity.Version) {
		t.Fatalf("identity version %q below baseline yet accepted", identity.Version)
	}
}
