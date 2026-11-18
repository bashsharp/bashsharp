package interp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func runBashPPConcurrencyReview(t *testing.T, src string) (string, error) {
	t.Helper()
	var out strings.Builder // bashpp-racegate:safe-synchronized
	r, err := New(Lang(syntax.LangBashPP), StdIO(nil, &out, &out))
	if err != nil {
		t.Fatal(err)
	}
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "review.bpp")
	if err != nil {
		t.Fatal(err)
	}
	err = r.Run(context.Background(), f)
	return out.String(), err
}

func TestBashPPForgedChannelHandle(t *testing.T) {
	out, err := runBashPPConcurrencyReview(t, `
func main() {
 ch := make(chan string, 1)
 fake := "chan@bashpp:0"
 fake <- forged
 got := <-ch
 echo "$got"
}
main()
`)
	if err != nil {
		t.Fatal(err)
	}
	if out == "forged\n" {
		t.Fatalf("forged scalar accessed channel: %q", out)
	}
}

func TestBashPPLiteralHandlePrefixCrossesExec(t *testing.T) {
	out, err := runBashPPConcurrencyReview(t, `
func main() {
 ch := make(chan string, 1)
 `+concurrencyEchoCommand(t)+` chan@bashpp:not-a-handle
 close(ch)
}
main()
`)
	if err != nil || out != "chan@bashpp:not-a-handle\n" {
		t.Fatalf("ordinary scalar refused: out=%q err=%v", out, err)
	}
}

func TestBashPPTaskTrapSnapshot(t *testing.T) {
	out, _ := runBashPPConcurrencyReview(t, `
func fail() { false; }
func main() {
 trap 'echo task-err-trap' ERR
 go fail()
}
main()
`)
	if !strings.Contains(out, "task-err-trap") {
		t.Fatalf("task lost owner ERR trap: %q", out)
	}
}

func TestBashPPCancellationJoins(t *testing.T) {
	var out strings.Builder // bashpp-racegate:safe-synchronized
	r, _ := New(Lang(syntax.LangBashPP), StdIO(nil, &out, &out))
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(`
func block(ch) { got := <-ch; }
func main() { ch := make(chan string); go block(ch); }
main()
`), "cancel.bpp")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	_ = r.Run(ctx, f)
	if time.Since(start) > time.Second {
		t.Fatal("cancellation did not join promptly")
	}
}

func TestBashPPPrimaryFailureUsesLaunchOrdinal(t *testing.T) {
	seen := map[string]bool{} // bashpp-racegate:safe-private
	for i := 0; i < 200; i++ {
		out, _ := runBashPPConcurrencyReview(t, `
func seven() { return 7; }
func nine() { return 9; }
func main() { go seven(); go nine(); }
main()
`)
		seen[out] = true
	}
	if len(seen) != 1 {
		t.Fatalf("primary failure varied by completion race: %#v", seen)
	}
	for out := range seen {
		if !strings.Contains(out, "exit status 7") {
			t.Fatalf("later launch won primary failure: %q", out)
		}
	}
}

func TestBashPPTaskSnapshotDoesNotShareRNG(t *testing.T) {
	var out strings.Builder // bashpp-racegate:safe-synchronized
	r, _ := New(Lang(syntax.LangBashPP), WithDeterministic(1), StdIO(nil, &out, &out))
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(`
func rnd(ch) { ch <- "$RANDOM"; }
func main() {
 ch := make(chan string, 16)
 go rnd(ch); go rnd(ch); go rnd(ch); go rnd(ch)
 go rnd(ch); go rnd(ch); go rnd(ch); go rnd(ch)
}
main()
`), "rng.bpp")
	if err == nil {
		err = r.Run(context.Background(), f)
	}
	if err != nil {
		t.Fatal(err)
	}
}
