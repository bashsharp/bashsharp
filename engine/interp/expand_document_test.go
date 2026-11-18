package interp_test

import (
	"context"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestRunnerExpandDocumentUsesLiveShellState(t *testing.T) {
	ctx := context.Background()
	r, err := interp.New()
	if err != nil {
		t.Fatal(err)
	}
	file, err := syntax.NewParser().Parse(strings.NewReader(
		"HOME=/tmp/live\nENV='${HOME}/$(printf expanded)'\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(ctx, file); err != nil {
		t.Fatal(err)
	}
	got, err := r.ExpandDocument(ctx, r.LiveVar("ENV").String())
	if err != nil {
		t.Fatal(err)
	}
	if want := "/tmp/live/expanded"; got != want {
		t.Fatalf("expanded document = %q, want %q", got, want)
	}
}
