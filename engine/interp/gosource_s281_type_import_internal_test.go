package interp

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Sprint: #281; Story: #924; Story-ID: 5d72d9344abd
func TestGoSourceS281GeneratedTypeOnlyImport(t *testing.T) {
	goBin := filepath.Join(runtime.GOROOT(), "bin", "go")
	source, err := bashPPNativeSource(context.Background(), bashPPEvalRequest{
		Go:  goBin,
		Dir: t.TempDir(),
		Env: os.Environ(),
		CompanionTrampolines: []bashPPCompanionTrampoline{{
			Name:    "splitSeqResult",
			Results: []string{"iter.Seq[string]"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(source, "iter.Seq") {
		t.Fatalf("generated helper retained unresolved iter qualifier:\n%s", source)
	}
	if !strings.Contains(source, `"iter"`) || !strings.Contains(source, ".Seq[string]") {
		t.Fatalf("generated helper did not synthesize an iter import:\n%s", source)
	}
	buildS270DependencyWorker(t, source, t.TempDir())
}
