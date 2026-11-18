package interp_test

import (
	"context"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestKeywordExportAssignmentDoesNotChangeShell(t *testing.T) {
	const src = "set -k; export HOME=/foo/bar; echo \"$HOME\""
	file, err := syntax.NewParser().Parse(strings.NewReader(src), "bash")
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	r, err := interp.New(
		interp.Env(expand.ListEnviron("HOME=/foo/original", "PATH=/bin:/usr/bin")),
		interp.StdIO(nil, &out, &out),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), file); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, `declare -x HOME="/foo/original"`+"\n") ||
		!strings.HasSuffix(got, "/foo/original\n") ||
		strings.Contains(got, `HOME="/foo/bar"`) {
		t.Fatalf("wrong keyword export behavior: %q", got)
	}
}
