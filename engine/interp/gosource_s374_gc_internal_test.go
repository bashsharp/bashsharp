//go:build full

package interp

import (
	"context"
	"runtime"
	"runtime/debug"
	"testing"
	"weak"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Explicit source GC must collect interpreter garbage even before any finalizer
// is installed, and after the finalizer table becomes idle. Disable automatic
// GC so an unrelated allocation cannot hide a missing host collection.
func TestS374SourceGCCollectsWithoutFinalizers(t *testing.T) {
	old := debug.SetGCPercent(-1)
	defer debug.SetGCPercent(old)
	for _, state := range []string{"no-concurrency", "no-table", "idle-table"} {
		t.Run(state, func(t *testing.T) {
			r := &Runner{bashPPGoSource: true, bashPPImports: map[string]string{"rt": "runtime"}}
			if state != "no-concurrency" {
				r.bashPPConcurrency(context.Background())
			}
			if state == "idle-table" {
				r.goSourceFinalizerTable(context.Background())
			}
			call := &syntax.BashPPCall{Fun: []*syntax.Lit{{Value: "rt"}, {Value: "GC"}}}
			var refs []weak.Pointer[[]byte]
			for batch := 0; batch < 2; batch++ {
				for i := 0; i < 4; i++ {
					refs = append(refs, s374DeadBuffer())
				}
				_, claimed, err := r.goSourceFinalizerCall(context.Background(), call)
				if err != nil || claimed {
					t.Fatalf("GC forwarding: claimed=%v err=%v", claimed, err)
				}
				live := 0
				for _, ref := range refs {
					if ref.Value() != nil {
						live++
					}
				}
				t.Logf("batch=%d allocated=%d KiB retained buffers=%d", batch+1, len(refs)*256, live)
				if live != 0 {
					t.Fatalf("explicit source GC retained %d dead buffers", live)
				}
			}
		})
	}
}

//go:noinline
func s374DeadBuffer() weak.Pointer[[]byte] {
	p := new([]byte)
	*p = make([]byte, 256<<10)
	ref := weak.Make(p)
	runtime.KeepAlive(p)
	return ref
}
