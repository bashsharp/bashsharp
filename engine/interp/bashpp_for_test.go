// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestBashPPForFormsScopeAndPerIterationVariables(t *testing.T) {
	const src = `func main() {
	outer := 9
	for false { echo wrong }
	for i := 0; i < 3; i++ {
		defer func() { echo "capture:$i" }()
		body := i
		echo "body:$body"
	}
	echo "after:$outer:${i-unset}:${body-unset}"
	for { echo infinite; return }
}
main()
`
	for _, bytewise := range []bool{false, true} {
		var rd interface{ Read([]byte) (int, error) } = strings.NewReader(src)
		if bytewise {
			rd = iotest.OneByteReader(strings.NewReader(src))
		}
		f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(rd, "")
		if err != nil {
			t.Fatalf("bytewise=%v: %v", bytewise, err)
		}
		var out strings.Builder
		r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
		if err := r.Run(context.Background(), f); err != nil {
			t.Fatalf("bytewise=%v: run: %v; output: %s", bytewise, err, out.String())
		}
		const want = "body:0\nbody:1\nbody:2\nafter:9:unset:unset\ninfinite\ncapture:2\ncapture:1\ncapture:0\n"
		if out.String() != want {
			t.Fatalf("bytewise=%v: output = %q, want %q", bytewise, out.String(), want)
		}
	}
}

func TestBashPPForAssignmentInitAndPost(t *testing.T) {
	const src = `func main() {
	i := 0
	for i = 1; i <= 3; i = i + 1 { echo $i }
	echo "after:$i"
}
main()
`
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
	if err := r.Run(context.Background(), f); err != nil {
		t.Fatalf("run: %v; output: %s", err, out.String())
	}
	if got, want := out.String(), "1\n2\n3\nafter:4\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestBashPPForRejectsNonBooleanCondition(t *testing.T) {
	const src = "func main() {\n\tfor 1 + 1 { echo wrong }\n}\nmain()\n"
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
	err = r.Run(context.Background(), f)
	var status interp.ExitStatus
	if !errors.As(err, &status) || status != 2 {
		t.Fatalf("Run error = %v, want exit status 2", err)
	}
	const want = "BASHPP-EFOR-COND: for condition must be boolean, got Int\n"
	if out.String() != want {
		t.Fatalf("diagnostic = %q, want %q", out.String(), want)
	}
}
