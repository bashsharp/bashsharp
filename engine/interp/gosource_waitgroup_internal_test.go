package interp

// Sprint: #118; Story: #54; Story-ID: c3a60493cde9
//
// The two guards that keep the WaitGroup.Go operation from generalising. They
// are unit-tested directly because the interesting cases — a same-named method
// on another type, a handle whose session is gone — are exactly the ones an
// end-to-end program cannot reach once the guard is doing its job.

import (
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestGoSourceWaitGroupHandle(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value bashPPBridgeValue
		want  bool
	}{
		{"value_waitgroup", bashPPBridgeValue{Kind: "handle", Session: "s", NativeType: "sync.WaitGroup", Type: "sync.WaitGroup"}, true},
		{"pointer_waitgroup", bashPPBridgeValue{Kind: "handle", Session: "s", NativeType: "*sync.WaitGroup", Type: "*sync.WaitGroup"}, true},
		// The dependency reports Type but not NativeType for some shapes; the
		// declared type is the fallback, never a substitute for a handle.
		{"declared_type_only", bashPPBridgeValue{Kind: "handle", Session: "s", Type: "sync.WaitGroup"}, true},
		// A native type whose name merely ends the same way is a different type.
		{"foreign_waitgroup", bashPPBridgeValue{Kind: "handle", Session: "s", NativeType: "golang.org/x/sync/errgroup.Group", Type: "errgroup.Group"}, false},
		{"other_sync_type", bashPPBridgeValue{Kind: "handle", Session: "s", NativeType: "sync.Once", Type: "sync.Once"}, false},
		{"original_local_type", bashPPBridgeValue{Kind: "handle", Session: "s", NativeType: "main.Runner", Type: "main.Runner"}, false},
		// A handle with no minting session can never name a live object, so it
		// must not be credited with a native Add.
		{"unbound_session", bashPPBridgeValue{Kind: "handle", NativeType: "sync.WaitGroup", Type: "sync.WaitGroup"}, false},
		// A decoded struct is a copy of the object, not the object.
		{"decoded_struct", bashPPBridgeValue{Kind: "struct", Session: "s", NativeType: "sync.WaitGroup", Type: "sync.WaitGroup"}, false},
		{"nil_value", bashPPBridgeValue{Kind: "nil", Session: "s", Type: "sync.WaitGroup"}, false},
		{"zero", bashPPBridgeValue{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := goSourceWaitGroupHandle(tc.value); got != tc.want {
				t.Fatalf("goSourceWaitGroupHandle(%+v) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

func TestGoSourceWaitGroupSelector(t *testing.T) {
	lit := func(s string) *syntax.Lit { return &syntax.Lit{Value: s} }
	ident := func(s string) *syntax.BashPPIdent { return &syntax.BashPPIdent{Name: lit(s)} }

	// wg.Go(…) as the parser spells an ordinary selector statement.
	base, ok := goSourceWaitGroupSelector(&syntax.BashPPCall{Fun: []*syntax.Lit{lit("wg"), lit("Go")}})
	if !ok {
		t.Fatal("wg.Go was not recognised")
	}
	if id, isIdent := base.(*syntax.BashPPIdent); !isIdent || id.Name.Value != "wg" {
		t.Fatalf("receiver = %#v, want ident wg", base)
	}

	// group.wg.Go(…): every literal but the last belongs to the receiver.
	base, ok = goSourceWaitGroupSelector(&syntax.BashPPCall{Fun: []*syntax.Lit{lit("g"), lit("wg"), lit("Go")}})
	if !ok {
		t.Fatal("g.wg.Go was not recognised")
	}
	sel, isSel := base.(*syntax.BashPPSelectorExpr)
	if !isSel || sel.Sel.Value != "wg" {
		t.Fatalf("receiver = %#v, want selector g.wg", base)
	}

	// A computed callee carries the same shape as an expression.
	base, ok = goSourceWaitGroupSelector(&syntax.BashPPCall{
		CalleeExpr: &syntax.BashPPSelectorExpr{X: ident("wg"), Sel: lit("Go")},
	})
	if !ok || base == nil {
		t.Fatal("computed wg.Go was not recognised")
	}

	for _, tc := range []struct {
		name string
		call *syntax.BashPPCall
	}{
		// A bare Go(f) has no receiver to authenticate at all.
		{"bare_call", &syntax.BashPPCall{Fun: []*syntax.Lit{lit("Go")}}},
		{"other_method", &syntax.BashPPCall{Fun: []*syntax.Lit{lit("wg"), lit("Wait")}}},
		{"other_computed_method", &syntax.BashPPCall{CalleeExpr: &syntax.BashPPSelectorExpr{X: ident("wg"), Sel: lit("Add")}}},
		{"computed_non_selector", &syntax.BashPPCall{CalleeExpr: ident("Go")}},
		{"empty", &syntax.BashPPCall{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := goSourceWaitGroupSelector(tc.call); ok {
				t.Fatalf("goSourceWaitGroupSelector claimed %#v", tc.call)
			}
		})
	}
}
