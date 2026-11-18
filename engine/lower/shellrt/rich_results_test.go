package shellrt

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// The declared source types of interp/bashpp_story204_residual_test.go,
// TestBashPPTupleAssignNamedRichResultsPreserveMetadata. `richCh` is an int in
// the source and stays an int here: nothing in this package rewrites a
// source-defined signature, and no test below converts a channel to it.
type richBox struct{ N int }
type richCh int

func richProgram(t *testing.T) *Program {
	t.Helper()
	program, err := NewProgram()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		program.Channels.Close()
		program.Session.Close()
	})
	return program
}

func richOwner(p *Program) *ResultOwner {
	return &ResultOwner{Channels: p.Channels, Session: p.Session}
}

func richSite() ValueSite {
	return ValueSite{File: "input.bpp", Name: "rich", Line: 12, Column: 2}
}

// richCaller allocates the frame the way the compiler must: at the call site,
// for exactly this invocation, and passes it to the callee. Nothing is left in
// a shared slot for a later call to pick up.
func richCaller(t *testing.T, p *Program, arity int) *ResultFrame {
	t.Helper()
	frame, err := NewResultFrame(richOwner(p), arity)
	if err != nil {
		t.Fatal(err)
	}
	return frame
}

// richCallee is the callee half of the reference source:
//
//	func rich() (ptr *Box, value Box, pipe Ch) {
//	 p := new(Box); p.N = 5
//	 b := Box{N: 6}
//	 ch := make(chan int, 1); ch <- 7
//	 return p, b, ch
//	}
//
// The named results keep their declared Go types. The third result's declared
// storage holds the callee's own `Ch` cell value; the channel travels beside it.
func richCallee(t *testing.T, p *Program, frame *ResultFrame, n int) *ResultFrame {
	t.Helper()
	// The callee's named result cells.
	var pipe richCh
	pointer := &richBox{N: n}
	value := richBox{N: n + 1}
	channel, err := MakeChannel[int](p.Channels, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := Send(p.Context, p.Session, p.Channels, channel, n+2); err != nil {
		t.Fatal(err)
	}
	if err := SetResult(frame, 0, pointer); err != nil {
		t.Fatal(err)
	}
	if err := SetResult(frame, 1, value); err != nil {
		t.Fatal(err)
	}
	if err := SetResultCapability(frame, 2, pipe, channel); err != nil {
		t.Fatal(err)
	}
	return frame
}

func richInvoke(t *testing.T, p *Program, n int) *ResultFrame {
	t.Helper()
	return richCallee(t, p, richCaller(t, p, 3), n)
}

// TestRichResultsReachTheirDeclaredDestinations is the reference shape: a
// pointer, a struct value and a channel arrive through one transfer, and the
// printf of the source reads 5:6:7 from them.
func TestRichResultsReachTheirDeclaredDestinations(t *testing.T) {
	program := richProgram(t)
	sidecars := NewResultSidecars()
	frame := richInvoke(t, program, 5)

	var pointer *richBox
	var value richBox
	var pipe richCh
	if err := TransferResults(frame, sidecars, []any{&pointer, &value, &pipe}, richSite()); err != nil {
		t.Fatalf("transfer: %v", err)
	}
	// The element type survives: `got` is an int, not an any the source would
	// have to re-derive.
	received, ok, err := ReceiveCapability[int](program.Context, sidecars, &pipe)
	if err != nil || !ok {
		t.Fatalf("capability receive: value=%v ok=%v err=%v", received, ok, err)
	}
	if got := fmt.Sprintf("%v:%v:%v", pointer.N, value.N, received+0); got != "5:6:7" {
		t.Fatalf("got %q, want %q", got, "5:6:7")
	}
	// The declared cell is an integer and was never given the channel's
	// address, its length or any other fabricated stand-in.
	if pipe != 0 {
		t.Fatalf("declared Ch storage is %d, want the source zero", pipe)
	}
}

// TestRichResultPresenceIsNotStatus separates what a callable produced from
// how it exited: a non-zero status neither erases a recorded result nor
// invents a missing one.
func TestRichResultPresenceIsNotStatus(t *testing.T) {
	program := richProgram(t)
	frame := richCaller(t, program, 2)
	if frame.Present(0) || frame.Present(1) {
		t.Fatal("a fresh frame reports results it never received")
	}
	if err := SetResult(frame, 0, 7); err != nil {
		t.Fatal(err)
	}
	program.SetStatus(3)
	if !frame.Present(0) {
		t.Fatal("a produced result disappeared with a failing status")
	}
	if frame.Present(1) {
		t.Fatal("an unproduced result appeared")
	}
	// A transfer of a frame with a missing result is refused rather than
	// completed with a zero.
	if err := TransferResults(frame, NewResultSidecars(), []any{new(int), new(int)}, richSite()); err == nil {
		t.Fatal("a frame with an unproduced result transferred anyway")
	}
	program.SetStatus(0)
}

// TestRichResultArityFollowsTheSource keeps the descriptor's width a property
// of the signature: this runtime imposes no cap of its own.
func TestRichResultArityFollowsTheSource(t *testing.T) {
	program := richProgram(t)
	const wide = 64
	frame := richCaller(t, program, wide)
	if frame.Arity() != wide || frame.Owner() == nil {
		t.Fatalf("frame reports arity %d owner %v", frame.Arity(), frame.Owner())
	}
	targets := make([]any, wide)
	for i := range wide {
		if err := SetResult(frame, i, i); err != nil {
			t.Fatal(err)
		}
		targets[i] = new(int)
	}
	if err := TransferResults(frame, NewResultSidecars(), targets, richSite()); err != nil {
		t.Fatalf("wide transfer: %v", err)
	}
	if got := *(targets[wide-1].(*int)); got != wide-1 {
		t.Fatalf("last result is %d, want %d", got, wide-1)
	}
	if _, err := NewResultFrame(richOwner(program), -1); err == nil {
		t.Fatal("a negative arity was accepted")
	}
	if err := SetResult(frame, wide, 1); err == nil {
		t.Fatal("a slot outside the frame was accepted")
	}
}

// TestRichResultTransferIsAtomic covers the failed-validation half: a transfer
// that cannot commit its values must leave both the earlier native values and
// the earlier capability binding exactly as they were.
func TestRichResultTransferIsAtomic(t *testing.T) {
	program := richProgram(t)
	sidecars := NewResultSidecars()

	var pipe richCh
	var kept int = 3
	first := richInvoke(t, program, 5)
	if err := TransferResults(first, sidecars, []any{new(*richBox), new(richBox), &pipe}, richSite()); err != nil {
		t.Fatalf("first transfer: %v", err)
	}

	// The second invocation produces its own channel, and a value the second
	// target cannot hold: a *Box result aimed at the int binding.
	second := richCaller(t, program, 2)
	replacement, err := MakeChannel[int](program.Channels, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := Send(program.Context, program.Session, program.Channels, replacement, 99); err != nil {
		t.Fatal(err)
	}
	if err := SetResultCapability(second, 0, richCh(0), replacement); err != nil {
		t.Fatal(err)
	}
	if err := SetResult(second, 1, &richBox{N: 1}); err != nil {
		t.Fatal(err)
	}
	err = TransferResults(second, sidecars, []any{&pipe, &kept}, richSite())
	var typeErr *TupleError
	if !errors.As(err, &typeErr) {
		t.Fatalf("transfer error is %v, want a tuple type error", err)
	}
	if kept != 3 {
		t.Fatalf("rolled-back binding is %d, want 3", kept)
	}
	// The capability sidecar rolled back with the values: the binding still
	// resolves to the first invocation's channel, not the replacement.
	received, ok, err := ReceiveCapability[int](program.Context, sidecars, &pipe)
	if err != nil || !ok {
		t.Fatalf("capability receive after rollback: %v %v %v", received, ok, err)
	}
	if received != 7 {
		t.Fatalf("received %v after rollback, want the original 7", received)
	}
}

// TestRichResultNestedInvocationsKeepSeparateFrames states the reason there is
// no ambient last-result slot: an inner invocation completing between an outer
// one's production and its transfer must not disturb it.
func TestRichResultNestedInvocationsKeepSeparateFrames(t *testing.T) {
	program := richProgram(t)
	sidecars := NewResultSidecars()

	outer := richInvoke(t, program, 5)
	inner := richInvoke(t, program, 50)

	var innerPipe richCh
	if err := TransferResults(inner, sidecars, []any{new(*richBox), new(richBox), &innerPipe}, richSite()); err != nil {
		t.Fatalf("inner transfer: %v", err)
	}
	var outerPointer *richBox
	var outerPipe richCh
	if err := TransferResults(outer, sidecars, []any{&outerPointer, new(richBox), &outerPipe}, richSite()); err != nil {
		t.Fatalf("outer transfer: %v", err)
	}
	if outerPointer.N != 5 {
		t.Fatalf("outer pointer is %d, want 5", outerPointer.N)
	}
	for _, tc := range []struct {
		name string
		pipe *richCh
		want int
	}{
		{"inner", &innerPipe, 52},
		{"outer", &outerPipe, 7},
	} {
		received, ok, err := ReceiveCapability[int](program.Context, sidecars, tc.pipe)
		if err != nil || !ok || received != tc.want {
			t.Fatalf("%s receive: %v %v %v, want %v", tc.name, received, ok, err, tc.want)
		}
	}
}

// TestRichResultConcurrentInvocationsAreIndependent runs several entries at
// once against one shared sidecar table. Each keeps its own frame and its own
// destinations, so no invocation can read another's capability.
func TestRichResultConcurrentInvocationsAreIndependent(t *testing.T) {
	program := richProgram(t)
	sidecars := NewResultSidecars()

	const entries = 8
	var wait sync.WaitGroup
	failures := make(chan error, entries)
	for i := range entries {
		wait.Add(1)
		go func() {
			defer wait.Done()
			frame := richInvoke(t, program, i*10)
			var pointer *richBox
			var pipe richCh
			if err := TransferResults(frame, sidecars, []any{&pointer, new(richBox), &pipe}, richSite()); err != nil {
				failures <- fmt.Errorf("entry %d transfer: %w", i, err)
				return
			}
			received, ok, err := ReceiveCapability[int](program.Context, sidecars, &pipe)
			if err != nil || !ok {
				failures <- fmt.Errorf("entry %d receive: %v %v", i, ok, err)
				return
			}
			if pointer.N != i*10 || received != i*10+2 {
				failures <- fmt.Errorf("entry %d observed %d/%d", i, pointer.N, received)
			}
		}()
	}
	wait.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
}

// TestRichResultCopyDoesNotCarryCapability is the point of keying a sidecar by
// storage identity: copying the declared `Ch` copies an integer and nothing
// else. Authority is not something a value copy can pick up, and a rebind is
// an explicit operation on real bindings.
func TestRichResultCopyDoesNotCarryCapability(t *testing.T) {
	program := richProgram(t)
	sidecars := NewResultSidecars()
	frame := richInvoke(t, program, 5)

	var pipe richCh
	if err := TransferResults(frame, sidecars, []any{new(*richBox), new(richBox), &pipe}, richSite()); err != nil {
		t.Fatal(err)
	}
	copied := pipe
	if copied != pipe {
		t.Fatalf("copy %d differs from source %d", copied, pipe)
	}
	if !HasCapability(sidecars, &pipe) {
		t.Fatal("the transferred binding lost its capability")
	}
	if HasCapability(sidecars, &copied) {
		t.Fatal("a value copy acquired the capability")
	}
	if _, _, err := ReceiveCapability[int](program.Context, sidecars, &copied); !errors.Is(err, errNoResultCapability) {
		t.Fatalf("receive through a plain copy returned %v", err)
	}
	// A real source assignment between bindings carries the metadata.
	if err := BindCapability(sidecars, &pipe, &copied); err != nil {
		t.Fatalf("explicit rebind: %v", err)
	}
	received, ok, err := ReceiveCapability[int](program.Context, sidecars, &copied)
	if err != nil || !ok || received != 7 {
		t.Fatalf("receive after rebind: %v %v %v", received, ok, err)
	}
	var other int
	if err := BindCapability(sidecars, &pipe, &other); err == nil {
		t.Fatal("a capability was bound to an unrelated carrier type")
	}
}

// TestRichResultStringCarrierHoldsAuthority is the shared C3 obligation: an
// ordinary `string` binding can carry channel authority, so nothing here is
// specialised to a carrier named Ch. A string that merely looks the same —
// the kind text interpolation produces — carries nothing.
func TestRichResultStringCarrierHoldsAuthority(t *testing.T) {
	program := richProgram(t)
	sidecars := NewResultSidecars()
	frame := richCaller(t, program, 1)
	channel, err := MakeChannel[string](program.Channels, 2)
	if err != nil {
		t.Fatal(err)
	}
	var carrier string
	if err := SetResultCapability(frame, 0, carrier, channel); err != nil {
		t.Fatal(err)
	}
	var assigned string
	if err := TransferResults(frame, sidecars, []any{&assigned}, richSite()); err != nil {
		t.Fatal(err)
	}
	// `assigned <- assigned`: the send reaches the channel the binding holds.
	if err := SendCapability(program.Context, sidecars, &assigned, "message"); err != nil {
		t.Fatalf("send through a string carrier: %v", err)
	}
	received, ok, err := ReceiveCapability[string](program.Context, sidecars, &assigned)
	if err != nil || !ok || received != "message" {
		t.Fatalf("receive through a string carrier: %q %v %v", received, ok, err)
	}
	// Interpolation produces a new string in different storage. It is equal,
	// and it has no authority.
	forged := fmt.Sprintf("%s", assigned)
	if forged != assigned {
		t.Fatalf("interpolated %q differs from %q", forged, assigned)
	}
	if HasCapability(sidecars, &forged) {
		t.Fatal("an interpolated string acquired channel authority")
	}
	if err := SendCapability(program.Context, sidecars, &forged, "forged"); !errors.Is(err, errNoResultCapability) {
		t.Fatalf("a forged send returned %v, want a refusal", err)
	}
}

// TestRichResultNilPayloads keeps a nil result a nil result. A nil pointer is
// an ordinary value and transfers; a nil channel is not authority and is
// refused where it is offered, rather than becoming a live-looking sidecar.
func TestRichResultNilPayloads(t *testing.T) {
	program := richProgram(t)
	sidecars := NewResultSidecars()
	frame := richCaller(t, program, 1)
	if err := SetResult(frame, 0, (*richBox)(nil)); err != nil {
		t.Fatal(err)
	}
	pointer := &richBox{N: 1}
	if err := TransferResults(frame, sidecars, []any{&pointer}, richSite()); err != nil {
		t.Fatalf("nil pointer transfer: %v", err)
	}
	if pointer != nil {
		t.Fatalf("nil result committed %v", pointer)
	}
	var nilChannel chan int
	if err := SetResultCapability(frame, 0, richCh(0), nilChannel); err == nil {
		t.Fatal("a nil channel was recorded as authority")
	}
	// A payload this runtime carries as neither a value nor a capability is
	// refused where it is offered.
	if err := SetResultCapability(frame, 0, richCh(0), func() {}); err == nil {
		t.Fatal("a closure was recorded as channel authority")
	}
}

// TestRichResultOwningScopeRevocation covers the end of an entry's authority.
// Once the owning scope is closed the declared integer is still there, and it
// still cannot be turned back into a channel. The refusal is the runtime's
// established channel diagnostic, not a new one.
func TestRichResultOwningScopeRevocation(t *testing.T) {
	program := richProgram(t)
	sidecars := NewResultSidecars()
	frame := richInvoke(t, program, 5)

	var pipe richCh
	if err := TransferResults(frame, sidecars, []any{new(*richBox), new(richBox), &pipe}, richSite()); err != nil {
		t.Fatal(err)
	}
	// A second invocation that produced its results just before the owner lost
	// authority: its transfer must be refused rather than half committed.
	late := richInvoke(t, program, 20)
	pointer := &richBox{N: 1}
	program.Channels.Close()

	if _, _, err := ReceiveCapability[int](program.Context, sidecars, &pipe); !errors.Is(err, ErrChannelScopeClosed) {
		t.Fatalf("receive after revocation returned %v", err)
	}
	if err := TransferResults(late, sidecars, []any{&pointer, new(richBox), new(richCh)}, richSite()); !errors.Is(err, ErrChannelScopeClosed) {
		t.Fatalf("transfer after revocation returned %v", err)
	}
	if pointer.N != 1 {
		t.Fatalf("a refused transfer committed %d", pointer.N)
	}

	// Explicit revocation drops the binding itself.
	RevokeOwner(sidecars, richOwner(program))
	if HasCapability(sidecars, &pipe) {
		t.Fatal("a binding survived its owner's revocation")
	}
}

// TestRichResultReceiveObservesCancellation keeps a cancelled wait from
// blocking forever, through the same Receive every other channel read uses.
func TestRichResultReceiveObservesCancellation(t *testing.T) {
	program := richProgram(t)
	sidecars := NewResultSidecars()
	frame := richInvoke(t, program, 5)
	var pipe richCh
	if err := TransferResults(frame, sidecars, []any{new(*richBox), new(richBox), &pipe}, richSite()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReceiveCapability[int](program.Context, sidecars, &pipe); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(program.Context)
	done := make(chan error, 1)
	go func() {
		_, _, err := ReceiveCapability[int](ctx, sidecars, &pipe)
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled receive returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a cancelled capability receive never returned")
	}
}

// TestRichResultForeignCapabilityRejected keeps one entry's scope from lending
// authority to another's frame.
func TestRichResultForeignCapabilityRejected(t *testing.T) {
	program := richProgram(t)
	other := richProgram(t)
	foreign, err := MakeChannel[int](other.Channels, 1)
	if err != nil {
		t.Fatal(err)
	}
	frame := richCaller(t, program, 1)
	if err := SetResultCapability(frame, 0, richCh(0), foreign); !errors.Is(err, ErrForeignChannel) {
		t.Fatalf("a foreign channel was recorded as %v", err)
	}
}

// TestRichResultForkFollowsTheSnapshot is the child-storage rule. The forked
// table is built through the same address map the child's storage was cloned
// with — no second value graph — and the child's own scope decides: a region
// that shares the owner's scope keeps the channel, a subshell with a fresh
// scope refuses the inherited handle exactly as Snapshot does.
func TestRichResultForkFollowsTheSnapshot(t *testing.T) {
	program := richProgram(t)
	sidecars := NewResultSidecars()
	frame := richInvoke(t, program, 5)
	var parent richCh
	if err := TransferResults(frame, sidecars, []any{new(*richBox), new(richBox), &parent}, richSite()); err != nil {
		t.Fatal(err)
	}

	fresh := &ChannelScope{}
	defer fresh.Close()
	var child richCh
	snapshot := NewSnapshot(program.Readonly, fresh)
	if err := Capture(snapshot, &parent, &child); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Clone(); err != nil {
		t.Fatal(err)
	}
	forked, err := ForkSidecars(sidecars, snapshot, &ResultOwner{Channels: fresh, Session: program.Session})
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	// The child binding is the cloned storage, not the parent's.
	if !HasCapability(forked, &child) {
		t.Fatal("the child binding did not receive the sidecar")
	}
	if HasCapability(forked, &parent) {
		t.Fatal("the forked table kept the parent's address")
	}
	// A subshell's fresh scope has no authority over an inherited channel.
	if _, _, err := ReceiveCapability[int](program.Context, forked, &child); !errors.Is(err, ErrForeignChannel) {
		t.Fatalf("a fresh scope accepted an inherited channel: %v", err)
	}
	// The parent's own table is untouched by the fork.
	received, ok, err := ReceiveCapability[int](program.Context, sidecars, &parent)
	if err != nil || !ok || received != 7 {
		t.Fatalf("the parent lost its channel: %v %v %v", received, ok, err)
	}

	// A region that shares the owner's scope — a task — keeps the channel.
	var shared richCh
	shareSnapshot := NewSnapshot(program.Readonly, program.Channels)
	if err := Capture(shareSnapshot, &parent, &shared); err != nil {
		t.Fatal(err)
	}
	if err := shareSnapshot.Clone(); err != nil {
		t.Fatal(err)
	}
	task, err := ForkSidecars(sidecars, shareSnapshot, richOwner(program))
	if err != nil {
		t.Fatalf("task fork: %v", err)
	}
	if err := SendCapability(program.Context, task, &shared, 11); err != nil {
		t.Fatalf("a task lost its owner's channel: %v", err)
	}
	if got, ok, err := ReceiveCapability[int](program.Context, sidecars, &parent); err != nil || !ok || got != 11 {
		t.Fatalf("the task's send did not reach the owner's channel: %v %v %v", got, ok, err)
	}

	// A fork needs a cloned snapshot and the child's scope; neither is guessed.
	if _, err := ForkSidecars(sidecars, nil, richOwner(program)); err == nil {
		t.Fatal("a fork without a snapshot was accepted")
	}
	if _, err := ForkSidecars(sidecars, snapshot, nil); err == nil {
		t.Fatal("a fork without a child scope was accepted")
	}
}

// TestRichResultNativeWrapperRejectsEscape is the public boundary decision: a
// result that exists only as capability metadata must not leave a public Go
// signature as a fabricated zero, and must not leave with its authority
// silently dropped.
func TestRichResultNativeWrapperRejectsEscape(t *testing.T) {
	program := richProgram(t)
	frame := richInvoke(t, program, 5)

	pointer, err := NativeResult[*richBox](frame, 0, richSite())
	if err != nil || pointer.N != 5 {
		t.Fatalf("direct result: %v %v", pointer, err)
	}
	carrier, err := NativeResult[richCh](frame, 2, richSite())
	if !errors.Is(err, ErrResultCapabilityEscape) {
		t.Fatalf("escaping capability returned %v", err)
	}
	if carrier != 0 {
		t.Fatalf("a refused escape still produced %d", carrier)
	}
	if !strings.Contains(err.Error(), "input.bpp: line 12") {
		t.Fatalf("escape diagnostic is unpositioned: %v", err)
	}
	// A wrapper asking for the wrong static type is refused too, not zeroed.
	if _, err := NativeResult[int](frame, 0, richSite()); err == nil {
		t.Fatal("a mistyped native result was accepted")
	}
	if _, err := NativeResult[int](frame, 9, richSite()); err == nil {
		t.Fatal("a native result outside the frame was accepted")
	}
}

// TestRichResultBlankTargetDiscardsExplicitly records what the blank
// identifier means for a capability: the source asked for the result to be
// dropped, so it is dropped here and nowhere else.
func TestRichResultBlankTargetDiscardsExplicitly(t *testing.T) {
	program := richProgram(t)
	sidecars := NewResultSidecars()
	frame := richInvoke(t, program, 5)
	if err := TransferResults(frame, sidecars, []any{nil, nil, nil}, richSite()); err != nil {
		t.Fatalf("blank transfer: %v", err)
	}
	var pipe richCh
	if HasCapability(sidecars, &pipe) {
		t.Fatal("a discarded capability was bound anyway")
	}
}

// TestRichResultReleaseKeepsScopeAuthority separates unbinding a cell from
// closing a channel: releasing a sidecar is not a close.
func TestRichResultReleaseKeepsScopeAuthority(t *testing.T) {
	program := richProgram(t)
	sidecars := NewResultSidecars()
	frame := richInvoke(t, program, 5)
	var first, second richCh
	if err := TransferResults(frame, sidecars, []any{new(*richBox), new(richBox), &first}, richSite()); err != nil {
		t.Fatal(err)
	}
	if err := BindCapability(sidecars, &first, &second); err != nil {
		t.Fatal(err)
	}
	ReleaseCapability(sidecars, &first)
	if HasCapability(sidecars, &first) {
		t.Fatal("released binding still resolves")
	}
	received, ok, err := ReceiveCapability[int](program.Context, sidecars, &second)
	if err != nil || !ok || received != 7 {
		t.Fatalf("the other binding lost its channel: %v %v %v", received, ok, err)
	}
}
