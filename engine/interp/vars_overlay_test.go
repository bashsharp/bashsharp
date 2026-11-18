// Sprint: #153; Story: S153.6; Story-ID: 11fffa6b51be

package interp

import (
	"testing"

	"github.com/go-quicktest/qt"

	"github.com/bashsharp/bashsharp/engine/expand"
)

// TestOverlayEnvironSkip checks the cached lookup jump over empty function
// scopes: a deep chain resolves globals and outer locals through it, a
// write-through lands in the global scope, and a scope that gains its first
// value while a child is chained on it — the one way the cache can go stale —
// is seen by the child on its next lookup.
func TestOverlayEnvironSkip(t *testing.T) {
	root := &overlayEnviron{}
	qt.Assert(t, qt.IsNil(root.Set("G", expand.Variable{Set: true, Kind: expand.String, Str: "global"})))
	outer := newFuncScopeEnviron(root, false)
	qt.Assert(t, qt.IsNil(outer.Set("L", expand.Variable{Set: true, Local: true, Kind: expand.String, Str: "outer"})))
	chain := expand.WriteEnviron(outer)
	var frames []*overlayEnviron
	for range 1000 {
		f := newFuncScopeEnviron(chain, false)
		frames = append(frames, f)
		chain = f
	}
	inner := frames[len(frames)-1]
	qt.Assert(t, qt.Equals(inner.Get("G").Str, "global"))
	qt.Assert(t, qt.Equals(inner.Get("L").Str, "outer"))
	qt.Assert(t, qt.IsFalse(inner.Get("missing").IsSet()))

	// A plain assignment from the innermost frame writes through to the
	// global scope; every empty frame is skipped, none is touched.
	qt.Assert(t, qt.IsNil(inner.Set("G", expand.Variable{Set: true, Kind: expand.String, Str: "written"})))
	qt.Assert(t, qt.Equals(root.Get("G").Str, "written"))
	for _, f := range frames {
		qt.Assert(t, qt.IsNil(f.values))
	}

	// The middle frame gains its first value while its children are still
	// chained on it. The innermost frame must see that local, and once the
	// local is unset again, the outer one.
	middle := frames[500]
	qt.Assert(t, qt.IsNil(middle.Set("L", expand.Variable{Set: true, Local: true, Kind: expand.String, Str: "middle"})))
	qt.Assert(t, qt.Equals(inner.Get("L").Str, "middle"))
	qt.Assert(t, qt.IsTrue(middle.unsetLocalFromChild("L")))
	qt.Assert(t, qt.Equals(inner.Get("L").Str, "outer"))

	// Releasing the frames restores the parent's child count.
	for i := len(frames) - 1; i >= 0; i-- {
		frames[i].release()
	}
	qt.Assert(t, qt.Equals(outer.liveChildren, 0))
}
