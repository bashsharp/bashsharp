//go:build full

package interp_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// TestS319ConstraintEvalMethodValue exercises the exact cmd/internal/testdir
// bridge shape: constraint.Parse returns an Expr interface backed by one of
// the dependency's recursive concrete nodes, and Eval receives a method value
// over an interpreted receiver.
//
// Sprint: #319; Story: #1088; Story-ID: 00446a7bd51a
func TestS319ConstraintEvalMethodValue(t *testing.T) {
	const source = `package main

import (
	"fmt"
	"go/build/constraint"
)

type context struct{ want string }

func (c *context) match(tag string) bool { return tag == c.want }

func main() {
	expr, err := constraint.Parse("//go:build linux && !windows")
	ctxt := &context{want: "linux"}
	fmt.Println(err == nil, expr.Eval(ctxt.match))
}
`
	dir := t.TempDir()
	path := filepath.Join(dir, "original.go")
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	oracle := runNativeOracle(t, dir, path, nil, "")
	got := runGoSourceRunner(t, dir, path, source, nil, "")
	if got.stdout != oracle.stdout || got.stderr != oracle.stderr || got.status != oracle.status {
		t.Fatalf("interp=%+v oracle=%+v", got, oracle)
	}
}

// TestS374ConstraintEvalTestdirExperimentTagSkip exercises cmd/internal/testdir's
// actual build-tag callback shape. On a toolchain without GOEXPERIMENT=simd,
// the goexperiment.simd half of this expression is false even on amd64, so the
// harness must skip the file instead of running it.
//
// Sprint: #374; Story: #1537; Story-ID: 875fb3251256
func TestS374ConstraintEvalTestdirExperimentTagSkip(t *testing.T) {
	const source = `package main

import (
	"fmt"
	"go/build"
	"go/build/constraint"
	"slices"
	"strings"
	"unicode"
)

type context struct {
	GOOS   string
	GOARCH string
}

func (ctxt *context) match(name string) bool {
	if name == "" {
		return false
	}
	for _, c := range name {
		if !unicode.IsLetter(c) && !unicode.IsDigit(c) && c != '_' && c != '.' {
			return false
		}
	}
	if slices.Contains(build.Default.ReleaseTags, name) {
		return true
	}
	if strings.HasPrefix(name, "goexperiment.") {
		return slices.Contains(build.Default.ToolTags, name)
	}
	if name == ctxt.GOOS || name == "gc" {
		return true
	}
	if name == ctxt.GOARCH {
		return true
	}
	return false
}

func main() {
	expr, err := constraint.Parse("//go:build goexperiment.simd && amd64")
	if err != nil {
		panic(err)
	}
	ctxt := &context{GOOS: "linux", GOARCH: "amd64"}
	fmt.Println(expr.Eval(ctxt.match))
}
`
	dir := t.TempDir()
	path := filepath.Join(dir, "original.go")
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	oracle := runNativeOracle(t, dir, path, nil, "")
	got := runGoSourceRunner(t, dir, path, source, nil, "")
	if got.stdout != oracle.stdout || got.stderr != oracle.stderr || got.status != oracle.status {
		t.Fatalf("interp=%+v oracle=%+v", got, oracle)
	}
}

// TestS319ConstraintEvalForeignImplementationRefuses proves that observed
// dispatch is not a package-wide name match. An external type can satisfy
// constraint.Expr by embedding it and overriding Eval; both a top-level value
// and one nested below a source-visible AndExpr must remain fail-closed.
//
// Sprint: #319; Story: #1088; Story-ID: 00446a7bd51a
func TestS319ConstraintEvalForeignImplementationRefuses(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/s319\n\ngo 1.27\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	evilDir := filepath.Join(dir, "evil")
	if err := os.Mkdir(evilDir, 0o700); err != nil {
		t.Fatal(err)
	}
	const evil = `package evil
import "go/build/constraint"
var Saved func(string) bool
type Evil struct{ constraint.Expr }
func (*Evil) Eval(ok func(string) bool) bool { Saved = ok; return false }
func Outer(inner constraint.Expr) constraint.Expr { return &Evil{Expr: inner} }
func Nested(inner constraint.Expr) constraint.Expr {
	return &constraint.AndExpr{X: &Evil{Expr: inner}, Y: inner}
}
`
	if err := os.WriteFile(filepath.Join(evilDir, "evil.go"), []byte(evil), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, factory := range []string{"Outer", "Nested"} {
		t.Run(factory, func(t *testing.T) {
			source := `package main
import (
	"example.com/s319/evil"
	"go/build/constraint"
)
type context struct{}
func (*context) match(string) bool { return true }
func main() {
	expr, _ := constraint.Parse("//go:build linux")
	expr = evil.` + factory + `(expr)
	expr.Eval((&context{}).match)
}
`
			path := filepath.Join(dir, strings.ToLower(factory)+".go")
			program, err := gosource.Parse(strings.NewReader(source), path, gosource.Options{RunMain: true, Importer: lower.NewModuleImporter(dir)})
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.GoSourceModuleDir(dir), interp.StdIO(nil, &output, &output))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			err = runner.Run(ctx, program.File)
			if err == nil || !strings.Contains(err.Error(), "callbacks are unsupported for Eval") {
				t.Fatalf("foreign implementation was not refused: %v", err)
			}
		})
	}
}
