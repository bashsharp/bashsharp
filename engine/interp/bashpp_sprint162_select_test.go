//go:build full

package interp_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestSprint162SelectReceiveAssignments(t *testing.T) {
	for _, name := range []string{"assignment_receive.go", "assignment_targets.go"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join("..", "syntax", "testdata", "sprint162", "select", name)
			source, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			program, err := gosource.Parse(bytes.NewReader(source), path, gosource.Options{RunMain: true})
			if err != nil {
				t.Fatalf("gosource.Parse: %v", err)
			}
			want, err := os.ReadFile(path + ".expected")
			if err != nil {
				t.Fatal(err)
			}
			var out, errout bytes.Buffer
			r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &out, &errout))
			if err != nil {
				t.Fatal(err)
			}
			if err := r.Run(context.Background(), program.File); err != nil || out.String() != string(want) || errout.Len() != 0 {
				t.Fatalf("run: err=%v stdout=%q stderr=%q", err, out.String(), errout.String())
			}
		})
	}
}
