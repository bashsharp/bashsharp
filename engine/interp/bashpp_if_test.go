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

func TestBashPPIfBooleanBranchesAndScopes(t *testing.T) {
	const src = `func main() {
	n := 9
	if n := 2; n > 2 {
		echo high
	} else if n == 2 {
		echo "equal:$n"
		branch := "yes"
		echo "$branch"
	} else {
		echo low
	}
	echo "after:$n:${branch-unset}"
	if false {
		echo wrong
	} else {
		echo fallback
	}
}
main()
`
	for _, bytewise := range []bool{false, true} {
		var rd = strings.NewReader(src)
		var input interface{ Read([]byte) (int, error) } = rd
		if bytewise {
			input = iotest.OneByteReader(rd)
		}
		f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(input, "")
		if err != nil {
			t.Fatalf("bytewise=%v: %v", bytewise, err)
		}
		var out strings.Builder
		r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
		if err := r.Run(context.Background(), f); err != nil {
			t.Fatalf("bytewise=%v: run: %v; output: %s", bytewise, err, out.String())
		}
		const want = "equal:2\nyes\nafter:9:unset\nfallback\n"
		if out.String() != want {
			t.Fatalf("bytewise=%v: output = %q, want %q", bytewise, out.String(), want)
		}
	}
}

func TestBashPPIfRejectsNonBooleanCondition(t *testing.T) {
	const src = "func main() {\n\tif 1 { echo wrong }\n}\nmain()\n"
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
	const want = "BASHPP-EIF-COND: if condition must be boolean, got Int\n"
	if out.String() != want {
		t.Fatalf("diagnostic = %q, want %q", out.String(), want)
	}
}
