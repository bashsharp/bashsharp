package interp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Sprint 165 S165.2 (D8 = (a)): the runtime resolver's identity-keyed branch.
// The reviewed inventory is not widened; with a declared identity an
// internal standard-library package is admitted exactly when cmd/go's rule
// admits it for that identity, and internal visibility for module packages
// is decided on the identity instead of the importer's directory.

func identityRequest(t *testing.T, identity string, testMain bool) bashPPEvalRequest {
	t.Helper()
	root := t.TempDir()
	writeImportFixture(t, root, "dep/go.mod", "module example.com/dep\n\ngo 1.25\n")
	writeImportFixture(t, root, "dep/internal/secret/secret.go", "package secret\n")
	writeImportFixture(t, root, "app/go.mod", "module example.com/app\n\ngo 1.25\n\nrequire example.com/dep v0.0.0\nreplace example.com/dep => ../dep\n")
	req := nativeResolveRequest(filepath.Join(root, "app"), "GOWORK=off", "GO111MODULE=on", "GOFLAGS=-mod=mod")
	req.ImportPath, req.TestMain = identity, testMain
	return req
}

func TestBashPPResolveIdentityAdmitsInternalStdlib(t *testing.T) {
	ctx := context.Background()
	eval := nativeBashPPEvaluator{}
	cases := []struct {
		name     string
		identity string
		testMain bool
		path     string
		wantName string
		refusal  string
	}{
		{"15 package rows: cmd/compile identity", "cmd/compile/internal/foo", false, "internal/buildcfg", "buildcfg", ""},
		{"cmd/go's <pkg>.test identity", "cmd/compile/internal/base.test", false, "internal/testenv", "testenv", ""},
		{"intrinsic.go: -p main", "main", false, "internal/runtime/sys", "sys", ""},
		{"dotted identity: the inventory's verdict", "example.com/app", false, "internal/buildcfg", "", "reviewed Go standard library"},
		{"the test main", "cmd/compile/internal/base.test", true, "testing/internal/testdeps", "testdeps", ""},
		{"the suffix is not the fact", "cmd/compile/internal/base.test", false, "testing/internal/testdeps", "", "reviewed Go standard library"},
		{"a user test main", "example.com/app.test", true, "testing/internal/testdeps", "testdeps", ""},
		{"the fact exempts testing/internal only", "example.com/app.test", true, "internal/testenv", "", "reviewed Go standard library"},
		{"nothing wider: a non-internal unreviewed path", "cmd/compile/internal/foo", false, "cmd/internal/objabi", "objabi", ""},
		{"nothing wider: a sibling command tree", "cmd/compile/internal/foo", false, "cmd/link/internal/ld", "", "reviewed Go standard library"},
		{"nothing wider: another command's internal tree", "cmd/compile/internal/foo", false, "cmd/go/internal/base", "", "reviewed Go standard library"},
		{"reviewed compiler root architecture import", "cmd/compile.test", true, "cmd/compile/internal/amd64", "amd64", ""},
		{"reviewed compiler package vendored telemetry", "cmd/compile/internal/base.test", true, "cmd/vendor/golang.org/x/telemetry/counter", "counter", ""},
		{"compiler test suffix without asserted fact", "cmd/compile.test", false, "cmd/compile/internal/amd64", "", "reviewed Go standard library"},
		{"sibling command cannot use compiler architecture", "cmd/link.test", true, "cmd/compile/internal/amd64", "", "reviewed Go standard library"},
		{"compiler cannot use sibling architecture", "cmd/compile.test", true, "cmd/compile/internal/arm64", "", "reviewed Go standard library"},
		{"compiler cannot use sibling vendored telemetry", "cmd/compile/internal/base.test", true, "cmd/vendor/golang.org/x/telemetry/counter/countertest", "", "reviewed Go standard library"},
		{"reviewed packages need no identity", "", false, "strings", "strings", ""},
		{"reviewed packages are unaffected by an identity", "example.com/app", false, "strings", "strings", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := identityRequest(t, c.identity, c.testMain)
			name, err := eval.Resolve(ctx, req, c.path)
			if c.refusal != "" {
				if err == nil || !strings.Contains(err.Error(), c.refusal) {
					t.Fatalf("Resolve(%q) as %q = %q, %v; want refusal %q", c.path, c.identity, name, err, c.refusal)
				}
				return
			}
			if err != nil || name != c.wantName {
				t.Fatalf("Resolve(%q) as %q = %q, %v; want %q", c.path, c.identity, name, err, c.wantName)
			}
		})
	}
}

func TestBashPPResolveIdentityModuleInternal(t *testing.T) {
	ctx := context.Background()
	eval := nativeBashPPEvaluator{}
	t.Run("no identity: the directory rule, unchanged", func(t *testing.T) {
		req := identityRequest(t, "", false)
		if _, err := eval.Resolve(ctx, req, "example.com/dep/internal/secret"); err == nil || !strings.Contains(err.Error(), "internal package outside allowed tree") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("foreign identity refused with cmd/go's wording", func(t *testing.T) {
		req := identityRequest(t, "example.com/app", false)
		_, err := eval.Resolve(ctx, req, "example.com/dep/internal/secret")
		if err == nil || err.Error() != `bash++ import "example.com/dep/internal/secret": use of internal package example.com/dep/internal/secret not allowed` {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("identity inside the parent admitted (cmd/go's module branch)", func(t *testing.T) {
		req := identityRequest(t, "example.com/dep/x", false)
		if name, err := eval.Resolve(ctx, req, "example.com/dep/internal/secret"); err != nil || name != "secret" {
			t.Fatalf("name=%q err=%v", name, err)
		}
	})
	t.Run("a standard identity does not reach a module's internal tree", func(t *testing.T) {
		req := identityRequest(t, "main", false)
		if _, err := eval.Resolve(ctx, req, "example.com/dep/internal/secret"); err == nil || !strings.Contains(err.Error(), "use of internal package") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("traversal and vendor rules stay with the directory rule", func(t *testing.T) {
		req := identityRequest(t, "example.com/dep/x", false)
		for importPath, match := range map[string]string{"../dep/pkg": "path traversal", "example.com/dep/vendor/x": "canonical path"} {
			if err := validateBashPPImportVisibilityFor(req, filepath.Join(req.Dir, "..", "dep"), importPath); err == nil || !strings.Contains(err.Error(), match) {
				t.Errorf("%q error = %v", importPath, err)
			}
		}
		vendorDir := filepath.Join(req.Dir, "..", "dep", "vendor", "example.com", "x")
		if err := os.MkdirAll(vendorDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := validateBashPPImportVisibilityFor(req, vendorDir, "example.com/x"); err == nil || !strings.Contains(err.Error(), "vendor package outside allowed tree") {
			t.Fatalf("vendor visibility error = %v", err)
		}
	})
}

func TestGoSourceIdentityOption(t *testing.T) {
	if _, err := New(Lang(syntax.LangBashPP), GoSourceIdentity("", true)); err == nil || !strings.Contains(err.Error(), "requires an import path") {
		t.Fatalf("err = %v, want the option refusal", err)
	}
	r, err := New(Lang(syntax.LangBashPP), GoSourceIdentity("cmd/compile/internal/base.test", true))
	if err != nil {
		t.Fatal(err)
	}
	r.Reset()
	r.bashPPTools.goBinary = "/reviewed/go"
	// The identity travels with the Go program only: shell source carries none.
	req, err := r.bashPPEvalRequest()
	if err != nil {
		t.Fatal(err)
	}
	if req.ImportPath != "" || req.TestMain {
		t.Fatalf("shell request carries an identity: %q %v", req.ImportPath, req.TestMain)
	}
	r.bashPPGoSource = true
	req, err = r.bashPPEvalRequest()
	if err != nil {
		t.Fatal(err)
	}
	if req.ImportPath != "cmd/compile/internal/base.test" || !req.TestMain {
		t.Fatalf("gosource request identity = %q testMain=%v", req.ImportPath, req.TestMain)
	}
}
