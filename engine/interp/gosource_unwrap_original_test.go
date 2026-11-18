//go:build full

package interp_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestGoSourceOriginalUnwrap(t *testing.T) {
	dir := filepath.Join(runtime.GOROOT(), "src", "errors")
	pins := map[string]string{
		"errors_test.go":  "7827d7e3ab7cc317a254ccf76158069b80c5b55b34ca45a3e6d799ad185f70ea",
		"example_test.go": "db75531e0575928a48a16d2c9769da59e719da091a31a4f03c3d5aeeca6be648",
		"join_test.go":    "09f9cc24acc7749d8f7197c6b90dc43223708c8f6412825e29ea3984bc6afdbc",
		"wrap_test.go":    "a098c0f752095df4614abf02925e52d2d35228e6ac272925dd4d9fd59f7e0cf6",
	}
	var sources []gosource.Source
	verify := func() {
		for name, digest := range pins {
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprintf("%x", sha256.Sum256(data)) != digest {
				t.Fatalf("requires unchanged Go1.27 source: %s", name)
			}
		}
	}
	verify()
	defer verify()
	for _, name := range []string{"errors_test.go", "example_test.go", "join_test.go", "wrap_test.go"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		sources = append(sources, gosource.Source{Name: filepath.Join(dir, name), Data: data})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	native := exec.CommandContext(ctx, "go", "test", "-p", "2", "errors", "-run", "^TestUnwrap$", "-count=1", "-v")
	if out, err := native.CombinedOutput(); err != nil || !strings.Contains(string(out), "--- PASS: TestUnwrap") {
		t.Fatalf("original native: %v %s", err, out)
	}
	program, err := gosource.Load(sources, gosource.Options{Importer: lower.NewModuleImporter(dir)})
	if err != nil {
		t.Fatal(err)
	}
	var out, errs bytes.Buffer
	r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(t.TempDir()), interp.StdIO(nil, &out, &errs))
	if err != nil {
		t.Fatal(err)
	}
	session, err := r.LoadGoSourceTests(ctx, program)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tests, err := session.Tests()
	if err != nil {
		t.Fatal(err)
	}
	if len(tests) != 10 {
		t.Fatalf("original roots: %d, want 10", len(tests))
	}
	t.Logf("registered all %d original roots; execute original TestUnwrap", len(tests))
	if err = session.Run(ctx, "TestUnwrap", t); err != nil {
		t.Fatalf("original TestUnwrap: %v stdout=%q stderr=%q", err, out.String(), errs.String())
	}
	if errs.Len() != 0 || out.Len() != 0 {
		t.Fatalf("unexpected original streams stdout=%q stderr=%q", out.String(), errs.String())
	}
}
