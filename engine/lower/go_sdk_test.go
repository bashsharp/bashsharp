package lower

import (
	"os/exec"
	"testing"

	"github.com/bashsharp/bashsharp/engine/polyglot"
)

// BASHPP_GO is the embedder's toolchain injection: it must be the first
// candidate tried, ahead of GOROOT and PATH.
func TestGoSDKCandidatesInjected(t *testing.T) {
	hostGo, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go on PATH")
	}
	t.Setenv("BASHPP_GO", hostGo)
	candidates, _ := goSDKCandidates()
	if len(candidates) == 0 || candidates[0].source != "BASHPP_GO" {
		t.Fatalf("candidates = %+v, want BASHPP_GO first", candidates)
	}
	if candidates[0].bin != hostGo {
		t.Fatalf("injected bin = %q, want %q", candidates[0].bin, hostGo)
	}

	sdk, err := resolveGoSDK(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if sdk.Source != "BASHPP_GO" {
		t.Fatalf("resolved via %q, want BASHPP_GO", sdk.Source)
	}
}

// A relative injection is rejected up front rather than exec'd against the
// current directory.
func TestGoSDKCandidatesInjectedRelativeRejected(t *testing.T) {
	t.Setenv("BASHPP_GO", "go")
	candidates, rejected := goSDKCandidates()
	for _, c := range candidates {
		if c.source == "BASHPP_GO" {
			t.Fatalf("relative BASHPP_GO accepted: %+v", c)
		}
	}
	found := false
	for _, reason := range rejected {
		if len(reason) >= len("BASHPP_GO") && reason[:len("BASHPP_GO")] == "BASHPP_GO" {
			found = true
		}
	}
	if !found {
		t.Fatalf("rejection reasons %v name no BASHPP_GO", rejected)
	}
}

func TestGoSDKCandidatesInjectedSkipsResolver(t *testing.T) {
	hostGo, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go on PATH")
	}
	previous := polyglot.ToolResolver
	defer func() { polyglot.ToolResolver = previous }()
	called := 0
	polyglot.ToolResolver = func(string) ([]string, string, error) {
		called++
		return []string{hostGo}, "test resolver", nil
	}
	t.Setenv("BASHPP_GO", hostGo)
	candidates, _ := goSDKCandidates()
	if called != 0 || len(candidates) != 1 || candidates[0].source != "BASHPP_GO" {
		t.Fatalf("valid override: resolver calls=%d candidates=%+v", called, candidates)
	}
	t.Setenv("BASHPP_GO", "go") // relative override is invalid; resolver remains the fallback
	candidates, _ = goSDKCandidates()
	if called != 1 || len(candidates) != 1 || candidates[0].source != "resolver" {
		t.Fatalf("invalid override: resolver calls=%d candidates=%+v", called, candidates)
	}
}
