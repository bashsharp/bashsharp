//go:build full

package interp_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestGoSourceUnwrapCancelReset(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out := &callbackCancelWriter{cancel: cancel}
	r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.StdIO(nil, out, out))
	if err != nil {
		t.Fatal(err)
	}
	load := func(source string) *syntax.File {
		p, err := gosource.Parse(strings.NewReader(source), filepath.Join(dir, "original.go"), gosource.Options{RunMain: true})
		if err != nil {
			t.Fatal(err)
		}
		return p.File
	}
	source := `package main
import("errors";"time")
type wrapped struct{}
func(w wrapped)Error()string{return "wrapped"}
func(w wrapped)Unwrap()error{println("callback-ready");time.Sleep(time.Minute);return nil}
func main(){errors.Unwrap(wrapped{});println("after")}`
	if err = r.Run(ctx, load(source)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v %q", err, out.String())
	}
	if strings.Contains(out.String(), "after") {
		t.Fatal("continued after cancellation")
	}
	r.Reset()
	out.Reset()
	source = `package main
import("fmt";"errors")
type wrapped struct{}
func(w wrapped)Error()string{return "wrapped"}
func(w wrapped)Unwrap()error{return nil}
func main(){fmt.Println(errors.Unwrap(wrapped{})==nil)}`
	if err = r.Run(context.Background(), load(source)); err != nil || out.String() != "true\n" {
		t.Fatalf("Reset: %v %q", err, out.String())
	}
}

func TestGoSourceUnwrapTransportBoundaries(t *testing.T) {
	for name, result := range map[string]string{"wrong_signature": "string", "multi_error": "[]error"} {
		t.Run(name, func(t *testing.T) {
			body := "return nil"
			if result == "string" {
				body = `return "wrong"`
			}
			source := `package main
import("fmt";"errors")
type wrapped struct{}
func(w wrapped)Error()string{return "wrapped"}
func(w wrapped)Unwrap()` + result + `{println("original-body");` + body + `}
func main(){fmt.Println(errors.Unwrap(wrapped{})==nil);println("after")}`
			dir := t.TempDir()
			path := filepath.Join(dir, "original.go")
			if err := os.WriteFile(path, []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("go", "run", path)
			cmd.Env = append(os.Environ(), "GOFLAGS=-p=2")
			if out, err := cmd.CombinedOutput(); err != nil || string(out) != "true\nafter\n" {
				t.Fatalf("oracle: %v %q", err, out)
			}
			p, err := gosource.Parse(strings.NewReader(source), path, gosource.Options{RunMain: true})
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.StdIO(nil, &out, &out))
			if err != nil {
				t.Fatal(err)
			}
			err = r.Run(context.Background(), p.File)
			// Since the S153.2 full method mirror these signatures cross: the
			// helper mirrors Unwrap at its original (non-error) signature, and
			// errors.Unwrap — which uses only Unwrap() error — never invokes
			// it, exactly as native Go never does. The boundary that remains
			// is that the original body must not run.
			if err != nil || out.String() != "true\nafter\n" {
				t.Fatalf("boundary: %v %q", err, out.String())
			}
			if strings.Contains(out.String(), "original-body") {
				t.Fatalf("unsupported execution: %q", out.String())
			}
		})
	}
}

func TestGoSourceUnwrapRetainedConsumer(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/unwrap\n\ngo 1.25.0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "dep"), 0700); err != nil {
		t.Fatal(err)
	}
	dependency := `package dep
import "fmt"
var Saved error
func Retain(e error){Saved=e;fmt.Println("retained")}`
	if err := os.WriteFile(filepath.Join(dir, "dep", "dep.go"), []byte(dependency), 0600); err != nil {
		t.Fatal(err)
	}
	source := `package main
import "example.com/unwrap/dep"
type wrapped struct{}
func(w wrapped)Error()string{return "wrapped"}
func(w wrapped)Unwrap()error{println("original-body");return nil}
func main(){dep.Retain(wrapped{});println("after")}`
	path := filepath.Join(dir, "original.go")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "run", "-p", "2", path)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil || string(out) != "retained\nafter\n" {
		t.Fatalf("oracle: %v %q", err, out)
	}
	var out bytes.Buffer
	r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.StdIO(nil, &out, &out))
	if err != nil {
		t.Fatal(err)
	}
	p, err := gosource.Parse(strings.NewReader(source), path, gosource.Options{RunMain: true, Importer: lower.NewModuleImporter(dir)})
	if err != nil {
		t.Fatal(err)
	}
	err = r.Run(context.Background(), p.File)
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("retainer: %v %q", err, out.String())
	}
	for _, marker := range []string{"retained", "after", "original-body"} {
		if strings.Contains(out.String(), marker) {
			t.Fatalf("retainer executed: %q", out.String())
		}
	}
}
