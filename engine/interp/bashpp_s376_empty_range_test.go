//go:build full

package interp_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Sprint: #376; Story: #1502; Story-ID: ac47500351df
func TestS376EmptyIntegerRangeEvaluatesBoundOnce(t *testing.T) {
	out, stderr, err := runGoSource(t, "empty-range", `package main
import "fmt"
var calls int
func bound() int { calls++; return 100000 }
func main() {
 for range bound() {}
 for _ = range 100000 {}
 for range -1 {}
 for i := range 3 { calls += i }
 fmt.Println(calls)
}
`)
	if err != nil || stderr != "" || out != "4\n" {
		t.Fatalf("run=%v stdout=%q stderr=%q", err, out, stderr)
	}
}

func TestS376EmptyIntegerRangeCancellation(t *testing.T) {
	p, err := gosource.Parse(strings.NewReader("package main\nfunc main() { for range 9223372036854775807 {} }\n"), "empty.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	r, err := interp.New(interp.Lang(syntax.LangBashPP))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := r.Run(ctx, p.File); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("empty range cancellation: %v", err)
	}
}

func BenchmarkS376EmptyIntegerRange(b *testing.B) {
	p, err := gosource.Parse(strings.NewReader("package main\nfunc main() { for range 1000000 {} }\n"), "empty.go", gosource.Options{RunMain: true})
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for range b.N {
		r, err := interp.New(interp.Lang(syntax.LangBashPP))
		if err != nil {
			b.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err = r.Run(ctx, p.File)
		cancel()
		if err != nil {
			b.Fatal(err)
		}
	}
}
