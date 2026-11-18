//go:build full

// Sprint: #219; Story: #462; Story-ID: e348d2c13248

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

// The front end refuses a statically wrong interface assignment before the
// runtime sees it, so the runtime's own refusals are probed directly on a
// live *bufio.Writer handle: a method the type lacks is missing, Flush with
// results is not `Flush()`, an unexported method is never a dependency
// type's, and the genuine Write signature — spelled with the byte alias —
// is admitted. Every answer is the dependency's; nothing is materialised.
func TestS219NativeInterfaceRefusals(t *testing.T) {
	var r *Runner
	var checks []error
	writer := callbackProbeWriter(func(p []byte) (int, error) {
		if !strings.Contains(string(p), "probe") {
			return len(p), nil
		}
		cell := r.bashPPScope.lookup("w")
		if cell == nil || !r.goSourceDependencyOwnedCell(cell) {
			checks = append(checks, fmt.Errorf("w is not a dependency-owned handle"))
			return len(p), nil
		}
		probe := func(name, want string) {
			iface, ok := r.bashPPInterfaceType(&syntax.BashPPNamedType{Name: &syntax.Lit{Value: name}})
			if !ok {
				checks = append(checks, fmt.Errorf("%s is not an interface", name))
				return
			}
			claimed, err := r.goSourceNativeImplements(cell, cell.declType, iface)
			if !claimed {
				checks = append(checks, fmt.Errorf("%s: handle admission not claimed", name))
				return
			}
			if want == "" && err != nil {
				checks = append(checks, fmt.Errorf("%s: want admission, got %v", name, err))
			}
			if want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
				checks = append(checks, fmt.Errorf("%s: want %q, got %v", name, want, err))
			}
		}
		probe("Writer", "")
		probe("Namer", "BASHPP-EINTERFACE-MISSING: *bufio.Writer does not implement interface (missing method Name)")
		probe("Flusher", "BASHPP-EINTERFACE-SIGNATURE: *bufio.Writer method Flush has wrong signature")
		probe("hidden", "missing method flush")
		return len(p), nil
	})
	var err error
	r, err = New(Lang(syntax.LangBashPP), Dir(t.TempDir()), StdIO(nil, io.Discard, writer))
	if err != nil {
		t.Fatal(err)
	}
	// w is used after the probe: a Go local stops being reachable at its last
	// use, and the interpreter drops its binding there.
	source := `package main
import ("bufio";"os")
type Writer interface { Write(p []byte) (n int, err error) }
type Namer interface { Name() string }
type Flusher interface { Flush() }
type hidden interface { flush() error }
var _ Writer
var _ Namer
var _ Flusher
var _ hidden
func main(){w:=bufio.NewWriter(os.Stdout);println("probe");w.Flush()}`
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
