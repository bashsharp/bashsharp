//go:build full

package interp

// Sprint: #250; Story: #140; Story-ID: 6495a054b527

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// TestGoSourceDeferredNativeMethodUsesOneRequest proves that defer captures a
// native receiver without first asking the dependency to manufacture a bound
// method handle. Direct and deferred Unlock therefore have the same bridge
// request count; the latter used to spend one extra request on every loop.
func TestGoSourceDeferredNativeMethodUsesOneRequest(t *testing.T) {
	requests := func(deferred bool) uint64 {
		t.Helper()
		unlock := "mu.Unlock()"
		if deferred {
			unlock = "defer mu.Unlock()"
		}
		source := "package main\nimport \"sync\"\nfunc main(){var mu sync.Mutex;mu.Lock();" + unlock + "}\n"
		dir := t.TempDir()
		program, err := gosource.Parse(strings.NewReader(source), filepath.Join(dir, "original.go"), gosource.Options{RunMain: true})
		if err != nil {
			t.Fatal(err)
		}
		r, err := New(Lang(syntax.LangBashPP), Dir(dir), StdIO(nil, io.Discard, io.Discard))
		if err != nil {
			t.Fatal(err)
		}
		session := &bashPPNativeSession{}
		r.bashPPTools.bridge = session
		if err := r.Run(context.Background(), program.File); err != nil {
			t.Fatal(err)
		}
		return session.next.Load()
	}

	direct := requests(false)
	deferred := requests(true)
	if deferred != direct {
		t.Fatalf("deferred native method used %d requests; direct call used %d", deferred, direct)
	}
}

// The receiver is evaluated when defer runs. Rebinding its source variable
// before the function returns must not redirect Unlock to the new mutex.
func TestGoSourceDeferredNativeMethodCapturesReceiver(t *testing.T) {
	source := `package main
import "sync"
func main() {
	var first, second sync.Mutex
	first.Lock()
	mu := &first
	func() {
		defer mu.Unlock()
		mu = &second
	}()
	if !first.TryLock() { panic("deferred Unlock used rebound mutex") }
	first.Unlock()
}
`
	dir := t.TempDir()
	program, err := gosource.Parse(strings.NewReader(source), filepath.Join(dir, "original.go"), gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	r, err := New(Lang(syntax.LangBashPP), Dir(dir), StdIO(nil, io.Discard, io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := r.Run(ctx, program.File); err != nil {
		t.Fatal(err)
	}
}
