package shellrt

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// labelKey carries a request label so the collector can report which region
// asked for assistance, not just how many did.
type labelKey struct{}

// requests is the deterministic collector standing in for cooperating
// in-process tools. Every context derived for a tool is recorded in call
// order, so a test can assert both the observations and their absence.
type requests struct {
	mu   sync.Mutex
	seen []string
}

func (r *requests) record(ctx context.Context) {
	label, _ := ctx.Value(labelKey{}).(string)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, fmt.Sprintf("%s:%t", label, Agentic(ctx)))
}

func (r *requests) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.seen...)
}

// tool is the one place a region consults assistance. It mirrors the engine's
// exec middleware reading interp.HandlerCtx(ctx).Agentic.
func (r *requests) tool(fr Frame, label string) {
	r.record(fr.Context(context.WithValue(context.Background(), labelKey{}, label)))
}

func restoreGlobals(t *testing.T) *bytes.Buffer {
	t.Helper()
	stderr, status, adapter := Stderr, Status, Adapter
	t.Cleanup(func() { Stderr, Status, Adapter = stderr, status, adapter })
	var buf bytes.Buffer
	Stderr = &buf
	Status = 0
	Adapter = nil
	return &buf
}

// TestFrameTransitions pins every transition of the contract. The expectations
// are the same observations the interpreter makes in
// interp.TestBashPPAgenticScopes, expressed as frames rather than runner state.
func TestFrameTransitions(t *testing.T) {
	block := Off().Block()
	for _, tc := range []struct {
		name string
		got  Frame
		want bool
	}{
		{"zero frame", Frame{}, false},
		{"off", Off(), false},
		{"source opt-in", Source(true), true},
		{"source without opt-in", Source(false), false},
		{"block from off", Off().Block(), true},
		{"nested block", block.Block(), true},
		{"eval inherits off", Off().Eval(), false},
		{"eval inherits block", block.Eval(), true},
		{"child copies off", Off().Child(), false},
		{"child copies block", block.Child(), true},
		{"defer captures off", Off().Defer(), false},
		{"defer captures block", block.Defer(), true},
		{"callback entry", Callback(), false},
		{"callback entry inside block", func() Frame { _ = block; return Callback() }(), false},
		{"file entry", File(), false},
		{"file entry inside block", func() Frame { _ = block; return File() }(), false},
	} {
		if got := tc.got.Agentic(); got != tc.want {
			t.Errorf("%s: got %t, want %t", tc.name, got, tc.want)
		}
	}
	// A transition never mutates the frame it was derived from.
	if !block.Agentic() {
		t.Fatal("derivations mutated their receiver")
	}
}

// TestEnterDerivesBodyFrameFromDeclaration checks that the body frame comes
// from the callable's marker, not from its caller: an ordinary helper or
// closure called inside a block still runs with assistance off.
func TestEnterDerivesBodyFrameFromDeclaration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		caller Frame
		marked bool
		want   bool
		denied bool
	}{
		{"marked from block", Off().Block(), true, true, false},
		{"marked from off", Off(), true, false, true},
		{"plain helper from block", Off().Block(), false, false, false},
		{"plain helper from off", Off(), false, false, false},
		{"closure from block", Off().Block(), false, false, false},
	} {
		body, err := tc.caller.Enter(Site{Name: "f"}, tc.marked)
		if tc.denied {
			var perr *PermissionError
			if !errors.As(err, &perr) {
				t.Errorf("%s: got %v, want a *PermissionError", tc.name, err)
			}
			if body.Agentic() {
				t.Errorf("%s: denied entry returned an agentic body frame", tc.name)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if got := body.Agentic(); got != tc.want {
			t.Errorf("%s: body frame %t, want %t", tc.name, got, tc.want)
		}
	}
}

// TestMarkerCheckPrecedesBody is the ordering guarantee: a denied call must
// produce no assistance request at all, because its body never runs.
func TestMarkerCheckPrecedesBody(t *testing.T) {
	restoreGlobals(t)
	reqs := &requests{}
	bodyRuns := 0
	// call mirrors the plumbing a compiled marked callable uses: check, then body.
	call := func(caller Frame, site Site) error {
		body, err := caller.Enter(site, true)
		if err != nil {
			return err
		}
		bodyRuns++
		reqs.tool(body, site.Name)
		return nil
	}
	if err := call(Off(), Site{Name: "denied"}); err == nil {
		t.Fatal("marked callable entered without an agentic caller")
	}
	if bodyRuns != 0 {
		t.Fatalf("body ran %d times before the marker check", bodyRuns)
	}
	if got := reqs.list(); len(got) != 0 {
		t.Fatalf("denied call made requests: %v", got)
	}
	if err := call(Off().Block(), Site{Name: "allowed"}); err != nil {
		t.Fatal(err)
	}
	// A later denial adds nothing after the allowed request.
	if err := call(Off(), Site{Name: "denied-again"}); err == nil {
		t.Fatal("second denial succeeded")
	}
	if got, want := reqs.list(), []string{"allowed:true"}; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// engineDenial runs the interpreter on source that calls a marked callable
// without an agentic caller, and returns its diagnostic and exit status.
func engineDenial(t *testing.T, name, src string, compat bool) (string, int) {
	t.Helper()
	var out strings.Builder
	options := []interp.RunnerOption{
		interp.Lang(syntax.LangBashPP),
		interp.StdIO(nil, &out, &out),
		interp.WithBashCompatErrors(compat),
	}
	r, err := interp.New(options...)
	if err != nil {
		t.Fatal(err)
	}
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), name)
	if err != nil {
		t.Fatal(err)
	}
	var status interp.ExitStatus
	if err := r.Run(context.Background(), file); !errors.As(err, &status) {
		t.Fatalf("got %v, want an exit status", err)
	}
	return out.String(), int(status)
}

// TestPermissionFailureMatchesEngine compares the runtime's denial against the
// engine's on equivalent source: identical bytes and identical status 1, in
// both diagnostic spellings.
func TestPermissionFailureMatchesEngine(t *testing.T) {
	buf := restoreGlobals(t)
	// The call statement is on line 3 in both scripts.
	const typed = "agentic func f() { echo hi }\n\nf()"
	const shell = "agentic function f() { echo hi; }\n\nf"
	for _, tc := range []struct {
		name   string
		file   string
		src    string
		compat bool
		site   Site
	}{
		{"typed caller", "agentic.bpp", typed, true, Site{Name: "f", File: "agentic.bpp", Line: 3}},
		{"shell caller", "agentic.bpp", shell, true, Site{Name: "f", File: "agentic.bpp", Line: 3}},
		{"unnamed input", "", typed, true, Site{Name: "f", Line: 3}},
		{"standalone entry", "agentic.bpp", typed, false, Site{Name: "f", File: "agentic.bpp"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want, wantStatus := engineDenial(t, tc.file, tc.src, tc.compat)
			buf.Reset()
			Status = 0
			Fail(&PermissionError{Site: tc.site})
			if got := buf.String(); got != want {
				t.Errorf("diagnostic\n got %q\nwant %q", got, want)
			}
			if Status != wantStatus {
				t.Errorf("status %d, want %d", Status, wantStatus)
			}
			if Status != 1 {
				t.Errorf("engine status %d, want 1", Status)
			}
		})
	}
}

// TestSitePositionSpellings documents the two forms directly: a caller reports
// its call statement, a standalone entry has none to report.
func TestSitePositionSpellings(t *testing.T) {
	const suffix = ": agentic action requires an explicit agentic { ...; } scope"
	for _, tc := range []struct {
		site Site
		want string
	}{
		{Site{Name: "f", File: "agentic.bpp", Line: 3}, "agentic.bpp: line 3: f" + suffix},
		{Site{Name: "f", Line: 12}, "bash: line 12: f" + suffix},
		{Site{Name: "f", File: "agentic.bpp"}, "f" + suffix},
		{Site{Name: "f"}, "f" + suffix},
		{Site{Name: "f", File: "agentic.bpp", Line: -1}, "f" + suffix},
	} {
		err := &PermissionError{Site: tc.site}
		if got := err.Error(); got != tc.want {
			t.Errorf("got %q, want %q", got, tc.want)
		}
	}
}

// TestRegionObservations threads one frame through the regions the contract
// names, and checks the requests a cooperating tool would see, in order.
func TestRegionObservations(t *testing.T) {
	restoreGlobals(t)
	reqs := &requests{}
	entry := Source(false) // a new file run starts with assistance off

	reqs.tool(entry, "before")
	block := entry.Block()
	reqs.tool(block, "inside")
	reqs.tool(block.Block(), "nested")

	// A marked callable entered from the block; its body opts in.
	marked, err := block.Enter(Site{Name: "marked"}, true)
	if err != nil {
		t.Fatal(err)
	}
	reqs.tool(marked, "marked")

	// A plain helper called from the block runs with assistance off until it
	// enters its own block.
	helper, err := block.Enter(Site{Name: "helper"}, false)
	if err != nil {
		t.Fatal(err)
	}
	reqs.tool(helper, "helper")
	reqs.tool(helper.Block(), "helper-own")

	// A deferred call retains its scheduling frame; a callback does not.
	scheduled := block.Defer()
	reqs.tool(Callback(), "callback")
	reqs.tool(block.Eval(), "eval")
	reqs.tool(block.Child(), "child")
	reqs.tool(File(), "sourced")
	reqs.tool(entry, "after")
	reqs.tool(scheduled, "deferred")

	want := []string{
		"before:false", "inside:true", "nested:true", "marked:true",
		"helper:false", "helper-own:true", "callback:false", "eval:true",
		"child:true", "sourced:false", "after:false", "deferred:true",
	}
	got := reqs.list()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("request %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

// TestFrameSurvivesUnwind covers restoration on panic, error return and
// cancellation. Restoration needs no bookkeeping: the caller's frame is a value
// the callee never received a reference to.
func TestFrameSurvivesUnwind(t *testing.T) {
	caller := Off().Block()
	func() {
		defer func() {
			if recover() == nil {
				t.Error("body did not panic")
			}
		}()
		body, err := caller.Enter(Site{Name: "boom"}, true)
		if err != nil {
			t.Fatal(err)
		}
		_ = body
		panic("boom")
	}()
	if !caller.Agentic() {
		t.Fatal("caller frame lost after a panic")
	}

	if _, err := caller.Enter(Site{Name: "f"}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := Off().Enter(Site{Name: "f"}, true); err == nil {
		t.Fatal("expected a denial")
	}
	if !caller.Agentic() {
		t.Fatal("caller frame lost after an error")
	}

	ctx, cancel := context.WithCancel(context.Background())
	derived := caller.Context(ctx)
	cancel()
	if !Agentic(derived) {
		t.Fatal("observation lost at cancellation")
	}
	if !caller.Agentic() {
		t.Fatal("caller frame lost after cancellation")
	}
	// Nothing about a frame is reachable after its region ends.
	if Off().Agentic() || Callback().Agentic() || File().Agentic() {
		t.Fatal("assistance leaked into a fresh entry frame")
	}
}

// TestChildFramesAreIndependent runs copies concurrently. With no goroutine
// identity, thread-local or mutable package scope behind Frame, this is
// race-free by construction; the test exists to keep it that way under -race.
func TestChildFramesAreIndependent(t *testing.T) {
	restoreGlobals(t)
	reqs := &requests{}
	parent := Off().Block()
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			child := parent.Child()
			if i%2 == 0 {
				// A task may leave assistance behind entirely.
				child = Callback()
			}
			reqs.tool(child, fmt.Sprintf("task%d", i))
			_, _ = child.Enter(Site{Name: "marked", File: "agentic.bpp", Line: i}, true)
		}()
	}
	wg.Wait()
	if !parent.Agentic() {
		t.Fatal("a child mutated its parent frame")
	}
	counts := map[string]int{}
	for _, r := range reqs.list() {
		counts[r[strings.IndexByte(r, ':')+1:]]++
	}
	if counts["true"] != 4 || counts["false"] != 4 {
		t.Fatalf("got %v, want 4 of each", counts)
	}
}

// TestDeferredCallsRetainSchedulingFrame covers both directions: a call
// scheduled inside a block runs with assistance on after the block is left,
// and one scheduled outside a block stays off even if a block ran in between.
func TestDeferredCallsRetainSchedulingFrame(t *testing.T) {
	restoreGlobals(t)
	reqs := &requests{}
	plainBody := Off()
	scheduledOutside := plainBody.Defer()
	block := plainBody.Block()
	scheduledInside := block.Defer()
	reqs.tool(plainBody, "body")
	// Deferred calls run last, in reverse scheduling order.
	marked, err := scheduledInside.Enter(Site{Name: "later"}, true)
	if err != nil {
		t.Fatal(err)
	}
	reqs.tool(marked, "later")
	if _, err := scheduledOutside.Enter(Site{Name: "denied"}, true); err == nil {
		t.Fatal("a call deferred outside a block reached a marked callable")
	}
	reqs.tool(scheduledOutside, "outside")
	want := []string{"body:false", "later:true", "outside:false"}
	got := reqs.list()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// TestContextObservation covers the default wiring and the embedder adapter.
func TestContextObservation(t *testing.T) {
	restoreGlobals(t)
	if Agentic(context.Background()) {
		t.Fatal("an underived context reported assistance")
	}
	if Agentic(nil) { //nolint:staticcheck // an explicit nil is the documented zero case
		t.Fatal("a nil context reported assistance")
	}
	if !Agentic(Off().Block().Context(context.Background())) {
		t.Fatal("block observation not carried")
	}
	if Agentic(Off().Context(context.Background())) {
		t.Fatal("off observation not carried")
	}
	if Off().Context(nil) == nil {
		t.Fatal("nil context not replaced")
	}

	// The adapter derives the context a differently keyed tool reads.
	type otherKey struct{}
	var seen []bool
	Adapter = func(ctx context.Context, agentic bool) context.Context {
		seen = append(seen, agentic)
		return context.WithValue(ctx, otherKey{}, agentic)
	}
	ctx := Off().Block().Context(context.Background())
	if v, _ := ctx.Value(otherKey{}).(bool); !v {
		t.Fatal("adapter context not returned")
	}
	if !Agentic(ctx) {
		t.Fatal("adapter dropped this package's observation")
	}
	if fmt.Sprint(seen) != "[true]" {
		t.Fatalf("adapter calls %v, want one true", seen)
	}

	// A nil result leaves the recorded context in place.
	Adapter = func(context.Context, bool) context.Context { return nil }
	if !Agentic(Off().Block().Context(context.Background())) {
		t.Fatal("nil adapter result dropped the observation")
	}
}
