//go:build full

package interp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestBashPPSprint162SelectInterfaceReceive(t *testing.T) {
	root := filepath.Join("testdata", "sprint162", "interp-control")
	source, err := os.ReadFile(filepath.Join(root, "select_interface.go"))
	if err != nil {
		t.Fatal(err)
	}
	program, err := gosource.Parse(strings.NewReader(string(source)), "select_interface.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	runner, err := New(Lang(syntax.LangBashPP), StdIO(nil, &out, &out))
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), program.File); err != nil {
		t.Fatalf("run: %v, output=%q", err, out.String())
	}
	want, err := os.ReadFile(filepath.Join(root, "select_interface.expected"))
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != string(want) {
		t.Fatalf("output %q, want %q", out.String(), string(want))
	}
}

func TestBashPPSprint162SelectInterfaceReceiveNegative(t *testing.T) {
	root := filepath.Join("testdata", "sprint162", "interp-control")
	source, err := os.ReadFile(filepath.Join(root, "select_interface_negative.go"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = gosource.Parse(strings.NewReader(string(source)), "select_interface_negative.go", gosource.Options{RunMain: true})
	if err == nil || !strings.Contains(err.Error(), "cannot use <-ch") {
		t.Fatalf("invalid receive assignment was not refused: %v", err)
	}
}
