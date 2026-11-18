//go:build full

package interp

import (
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// TestS219ChannelHotpath covers the direct channel-element lookup repeated by
// chanlinear.go's append loop. The structured type is already canonical, so
// resolving it must not take the named-type walk on every append.
func TestS219ChannelHotpath(t *testing.T) {
	r := &Runner{bashPPGoSource: true, bashPPTypes: make(map[string]bashPPType)}
	want := &syntax.BashPPChanType{
		Element: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "bool"}},
	}

	if got, ok := r.goSourceChannelType(want); !ok || got != want {
		t.Fatalf("direct channel lookup = (%p, %v), want (%p, true)", got, ok, want)
	}
	allocs := testing.AllocsPerRun(10_000, func() {
		if got, ok := r.goSourceChannelType(want); !ok || got != want {
			panic("direct channel lookup changed identity")
		}
	})
	if allocs != 0 {
		t.Fatalf("direct channel lookup allocated %.2f times per call, want 0", allocs)
	}

	r.bashPPTypes["NamedChan"] = bashPPType{typeExpr: want}
	named := &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "NamedChan"}}
	if got, ok := r.goSourceChannelType(named); !ok || got != want {
		t.Fatalf("named channel lookup = (%p, %v), want (%p, true)", got, ok, want)
	}
}

func BenchmarkS219ChannelHotpath(b *testing.B) {
	r := &Runner{bashPPGoSource: true}
	typ := &syntax.BashPPChanType{
		Element: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "bool"}},
	}
	b.Run("direct", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_, _ = r.goSourceChannelType(typ)
		}
	})
	b.Run("former-underlying-walk", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_, _ = r.bashPPUnderlyingType(typ).(*syntax.BashPPChanType)
		}
	})
}
