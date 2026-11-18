package interp

import (
	"context"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestGoSourceDeferredCloseDoesNotClaimClassic(t *testing.T) {
	r := &Runner{}
	call := &syntax.BashPPCall{Fun: []*syntax.Lit{{Value: "close"}}}
	if invoke, handled := r.goSourceCaptureDeferredClose(call); handled || invoke != nil {
		t.Fatal("GoSource close claimed a classic call")
	}
	if !r.exit.ok() || len(r.bashPPDeferStack) != 0 {
		t.Fatal("classic call acquired GoSource side effects")
	}
}

func TestGoSourceDeferredBuiltinFailureDuringPanic(t *testing.T) {
	r := &Runner{bashPPPanic: bashPPPanicState{active: true, chain: []string{"body"}}}
	r.bashPPDeferStack = []bashPPDeferred{
		{builtin: func() { r.bashPPPanic = bashPPPanicState{}; r.exit = exitStatus{} }},
		{builtin: func() { r.exit = exitStatus{code: 9} }},
	}
	r.bashPPRunDefers(context.Background(), 0)
	if r.exit.code != 9 {
		t.Fatalf("recovering body panic hid a distinct deferred operation failure: %v", r.exit)
	}
}
