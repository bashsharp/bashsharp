//go:build full

package interp_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func runSprint153Concurrency(t *testing.T, name string) (string, string, error) {
	t.Helper()
	sourcePath := filepath.Join("testdata", "sprint153", "concurrency", name)
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	program, err := gosource.Parse(bytes.NewReader(source), sourcePath, gosource.Options{RunMain: true})
	if err != nil {
		t.Fatalf("gosource.Parse: %v", err)
	}
	var out, errout bytes.Buffer
	r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &out, &errout))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	goroutines := len(cgSuspectGoroutines())
	runErr := r.Run(ctx, program.File)
	r.Reset()
	cgDrainToBaseline(t, goroutines, name, 2*time.Second)
	return out.String(), errout.String(), runErr
}

func TestSprint153DeferredReceiveCopy(t *testing.T) {
	for _, name := range []string{"deferred_receive_supported.go", "deferred_receive_copy.go"} {
		t.Run(name, func(t *testing.T) {
			out, errout, err := runSprint153Concurrency(t, name)
			if err != nil || out != "1\n" || errout != "" {
				t.Fatalf("run: err=%v stdout=%q stderr=%q", err, out, errout)
			}
		})
	}
}
