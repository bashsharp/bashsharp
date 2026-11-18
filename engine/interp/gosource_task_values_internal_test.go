//go:build full

package interp

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Obtain descriptors only from actual original-source Runner executions. A
// second session must reject the first session's object, including when its
// original variable has otherwise valid typed metadata.
func TestGoSourceCapturedNativeVariableSessions(t *testing.T) {
	var old *bashPPCell
	for round := 0; round < 2; round++ {
		var r *Runner
		var observed error
		writer := callbackProbeWriter(func(p []byte) (int, error) {
			if !bytes.Contains(p, []byte("probe")) {
				return len(p), nil
			}
			cell := r.bashPPScope.lookup("v")
			if cell == nil {
				t.Error("missing original native variable")
				return len(p), nil
			}
			if round == 0 {
				copy := *cell
				old = &copy
				if !r.bashPPGoSourceSharable(cell) {
					t.Errorf("live original cell refused: %v", r.exit.err)
				}
			} else {
				if r.bashPPGoSourceSharable(old) {
					t.Error("foreign session object was admitted")
				}
				observed = r.exit.err
			}
			return len(p), nil
		})
		var err error
		r, err = New(Lang(syntax.LangBashPP), Dir(t.TempDir()), StdIO(nil, nil, writer))
		if err != nil {
			t.Fatal(err)
		}
		p, err := gosource.Parse(strings.NewReader(`package main;import "math/big";func main(){v:=big.NewInt(7);println("probe");_ = v}`), "capture-session.go", gosource.Options{RunMain: true})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err = r.Run(ctx, p.File)
		cancel()
		if round == 0 {
			if err != nil {
				t.Fatal(err)
			}
			r.Reset()
			if r.bashPPGoSourceSharable(old) || r.exit.err == nil || !strings.Contains(r.exit.err.Error(), "closed dependency session") {
				t.Fatalf("Reset admitted stale captured object: %v", r.exit.err)
			}
		} else if observed == nil || !strings.Contains(observed.Error(), "another dependency session") {
			t.Fatalf("cross-session capture diagnostic: %v (Run %v)", observed, err)
		}
	}
}
