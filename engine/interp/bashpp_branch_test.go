// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp_test

import (
	"context"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestBashPPBranchNestedTargetsPostAndScopes(t *testing.T) {
	const src = `func main() {
	for i := 0; i < 5; i++ {
		body := i
		switch i {
		case 0:
			continue
		case 1:
			fallthrough
		case 99:
			echo "fell:$body"
		case 2:
			break
		default:
			echo "body:$body"
		}
		if i == 3 { break }
		echo "tail:$i"
	}
	echo done
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
		const want = "fell:1\ntail:1\ntail:2\nbody:3\ndone\n"
		if out.String() != want {
			t.Fatalf("bytewise=%v: output = %q, want %q", bytewise, out.String(), want)
		}
	}
}

func TestBashPPContinueCrossesSelectAndRangeBreak(t *testing.T) {
	const src = `func worker(ch) {
	ch <- 1
	ch <- 2
	close(ch)
}
func main() {
	for i := 0; i < 2; i++ {
		select { default: continue }
		echo wrong
	}
	echo post-ran
	ch := make(chan int, 2)
	go worker(ch)
	for v := range ch {
		if v == 1 { continue }
		echo $v
		break
	}
	echo range-broke
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
	if got, want := out.String(), "post-ran\n2\nrange-broke\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}
