package interp

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBashPPSourceOnlyGOPATHVendorContext(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, "src/app")
	writeImportFixture(t, root, "src/app/main.bpp", "import \"example.test/dep/pkg\"\nvendored.Print()\n")
	writeImportFixture(t, root, "src/app/message.txt", "relative sidecar\n")
	writeImportFixture(t, root, "src/app/vendor/example.test/dep/pkg/p.go", `package vendored
import("fmt";"os")
func Print() { b,e:=os.ReadFile("message.txt"); if e!=nil { panic(e) }; fmt.Print(string(b)) }
`)
	writeImportFixture(t, root, "src/example.test/dep/pkg/p.go", "package wrongglobal\nfunc Print() { panic(\"global package shadowed vendor\") }\n")
	writeImportFixture(t, root, "src/app/vendor/example.test/dep/internal/secret/p.go", "package secret\nconst N=7\n")
	writeImportFixture(t, root, "src/app/internal/allowed/p.go", "package allowed\nconst N=7\n")
	// Per-runner GOPATH mode must win over the embedding process environment.
	t.Setenv("GO111MODULE", "on")
	t.Setenv("GOPATH", t.TempDir())
	req := nativeResolveRequest(app, "GO111MODULE=off", "GOPATH="+root, "GOWORK=off", "GOTOOLCHAIN=local")
	ctx := context.Background()
	for _, eval := range []bashPPEvaluator{nativeBashPPEvaluator{}, newPolicyBashPPEvaluator()} {
		name, err := eval.Resolve(ctx, req, "example.test/dep/pkg")
		if err != nil || name != "vendored" {
			t.Fatalf("vendor name=%q err=%v", name, err)
		}
		if _, err := eval.Resolve(ctx, req, "example.test/dep/internal/secret"); err == nil || !strings.Contains(err.Error(), "internal") {
			t.Fatalf("external internal package: %v", err)
		}
		if _, err := eval.Resolve(ctx, req, "app/internal/allowed"); err != nil {
			t.Fatalf("own internal package: %v", err)
		}
	}
	var out, stderr bytes.Buffer
	req.Stdout, req.Stderr = &out, &stderr
	req.Imports = map[string]string{"vendored": "example.test/dep/pkg"}
	req.Selector = []string{"vendored", "Print"}
	if err := (nativeBashPPEvaluator{}).Call(ctx, req); err != nil || out.String() != "relative sidecar\n" || stderr.Len() != 0 {
		t.Fatalf("call stdout=%q stderr=%q status=%v", out.String(), stderr.String(), err)
	}
	entries, err := os.ReadDir(app)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".go") || strings.HasPrefix(entry.Name(), ".bashpp-eval-") {
			t.Fatalf("left bridge source %s", entry.Name())
		}
	}
}
