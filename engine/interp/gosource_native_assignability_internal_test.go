//go:build full

package interp

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestGoSourceNativeAssignableAuthenticatedValue(t *testing.T) {
	var r *Runner
	var checks []error
	writer := callbackProbeWriter(func(p []byte) (int, error) {
		if !strings.Contains(string(p), "probe") {
			return len(p), nil
		}
		aliases := map[string]string{}
		for alias, path := range r.bashPPImports {
			aliases[path] = alias
		}
		readerType := &syntax.BashPPNamedType{Name: &syntax.Lit{Value: aliases["io"] + ".Reader"}}
		good := r.bashPPNativeCellValue("good")
		bad := r.bashPPNativeCellValue("bad")
		if good == nil || bad == nil {
			checks = append(checks, fmt.Errorf("missing native fixtures"))
			return len(p), nil
		}
		if err := r.goSourceCheckNativeElement(good, readerType); err != nil {
			checks = append(checks, err)
		}
		forged := *bad
		forged.Type = "io.Reader"
		forged.NativeType = "io.Reader"
		if err := r.goSourceCheckNativeElement(&forged, readerType); err == nil || !strings.Contains(err.Error(), "cannot use native") {
			checks = append(checks, fmt.Errorf("forged concrete type accepted: %v", err))
		}
		stale := *good
		stale.Session += "-stale"
		if err := r.goSourceCheckNativeElement(&stale, readerType); err == nil || !strings.Contains(err.Error(), "another dependency session") {
			checks = append(checks, fmt.Errorf("stale native type accepted: %v", err))
		}
		invalid := &syntax.BashPPNamedType{Name: &syntax.Lit{Value: aliases["io"] + ".Missing"}}
		if err := r.goSourceCheckNativeElement(good, invalid); err == nil || !strings.Contains(err.Error(), "unregistered bridge type") {
			checks = append(checks, fmt.Errorf("missing native type accepted: %v", err))
		}
		return len(p), nil
	})
	var err error
	r, err = New(Lang(syntax.LangBashPP), Dir(t.TempDir()), StdIO(nil, io.Discard, writer))
	if err != nil {
		t.Fatal(err)
	}
	// good and bad are used after the probe: a Go local stops being reachable
	// at its last use, and the interpreter drops its binding there.
	source := `package main
import("fmt";"io";"strings";"time")
func main(){good:=strings.NewReader("x");bad:=time.Now();println("probe");fmt.Fprintf(io.Discard,"%T %T",good,bad)}`
	p, err := gosource.Parse(strings.NewReader(source), filepath.Join(r.Dir, "original.go"), gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Run(context.Background(), p.File); err != nil {
		t.Fatal(err)
	}
	for _, err := range checks {
		t.Error(err)
	}
}
