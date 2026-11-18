// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

package interp_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestRunFileCompletesBuiltinBackgroundHandoff(t *testing.T) {
	t.Parallel()
	out := filepath.Join(t.TempDir(), "out")
	src := "echo marker > " + strconv.Quote(out) + " &"
	file, err := syntax.NewParser().Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	runner, err := interp.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), file); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("background builtin was lost when the file run returned: %v", err)
	}
	if string(got) != "marker\n" {
		t.Fatalf("background output = %q, want marker newline", got)
	}
}
