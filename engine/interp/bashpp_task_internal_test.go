// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func liveNativeScope() bashPPNativeHandleScope {
	return bashPPNativeHandleScope{known: true, session: &bashPPNativeSession{}}
}

// A task must address the same native object as its parent: the descriptor is
// copied so no interpreter heap is shared, but Session and Handle are carried
// across verbatim. Copying the object instead would split a sync.WaitGroup and
// hang the parent in Wait.
func TestBashPPTaskCloneKeepsNativeHandleIdentity(t *testing.T) {
	cloner := newBashPPObjectCloner()
	cloner.native = liveNativeScope()
	original := &bashPPBridgeValue{Kind: "handle", Type: "sync.WaitGroup", Session: "0123456789abcdef", Handle: 7}
	cloned, err := cloner.clone(original)
	if err != nil {
		t.Fatal(err)
	}
	value, ok := cloned.(*bashPPBridgeValue)
	if !ok {
		t.Fatalf("clone gave %T, want *bashPPBridgeValue", cloned)
	}
	if value == original {
		t.Fatal("clone shared the parent's descriptor")
	}
	if value.Session != original.Session || value.Handle != original.Handle {
		t.Fatalf("clone = session %q handle %d, want session %q handle %d",
			value.Session, value.Handle, original.Session, original.Handle)
	}
	if value.Kind != original.Kind || value.Type != original.Type {
		t.Fatalf("clone = %+v, want kind/type of %+v", value, original)
	}
}

// Two shell names for one native object must stay two names for that same
// object inside the task, exactly as aliased maps and slices do.
func TestBashPPTaskCloneKeepsNativeHandleAliasing(t *testing.T) {
	cloner := newBashPPObjectCloner()
	cloner.native = liveNativeScope()
	original := &bashPPBridgeValue{Kind: "handle", Type: "sync.Mutex", Session: "0123456789abcdef", Handle: 3}
	first, err := cloner.clone(original)
	if err != nil {
		t.Fatal(err)
	}
	second, err := cloner.clone(original)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("aliased handle cloned twice: %p and %p", first, second)
	}
}

// The descriptor's own storage is duplicated, so a task writing through it
// cannot race the parent. Nested handles still name the parent's objects.
func TestBashPPTaskCloneCopiesNativeDescriptorStorage(t *testing.T) {
	cloner := newBashPPObjectCloner()
	cloner.native = liveNativeScope()
	original := &bashPPBridgeValue{
		Kind:     "struct",
		Type:     "pkg.Group",
		Elements: []bashPPBridgeValue{{Kind: "handle", Session: "0123456789abcdef", Handle: 1}},
		Fields:   map[string]bashPPBridgeValue{"wg": {Kind: "handle", Session: "0123456789abcdef", Handle: 2}},
		Entries: []bashPPBridgeEntry{{
			Key:   bashPPBridgeValue{Kind: "string", Text: "k"},
			Value: bashPPBridgeValue{Kind: "handle", Session: "0123456789abcdef", Handle: 4},
		}},
	}
	cloned, err := cloner.clone(original)
	if err != nil {
		t.Fatal(err)
	}
	value := cloned.(*bashPPBridgeValue)
	if value.Elements[0].Handle != 1 || value.Fields["wg"].Handle != 2 || value.Entries[0].Value.Handle != 4 {
		t.Fatalf("nested handles not preserved: %+v", value)
	}
	value.Elements[0].Text = "task"
	value.Fields["other"] = bashPPBridgeValue{Kind: "nil"}
	value.Entries[0].Key.Text = "task"
	if original.Elements[0].Text != "" || len(original.Fields) != 1 || original.Entries[0].Key.Text != "k" {
		t.Fatalf("task writes reached the parent descriptor: %+v", original)
	}
}

// A handle with no minting session can never name a native object, and one
// whose session has been closed is stale. Both fail closed at the snapshot
// instead of being forwarded to whichever session the task starts next.
func TestBashPPTaskCloneRefusesUnusableNativeHandles(t *testing.T) {
	for _, test := range []struct {
		name  string
		scope bashPPNativeHandleScope
		value *bashPPBridgeValue
		want  error
	}{
		{
			name:  "unbound",
			scope: liveNativeScope(),
			value: &bashPPBridgeValue{Kind: "handle", Type: "sync.WaitGroup", Handle: 9},
			want:  errBashPPUnboundNativeHandle,
		},
		{
			name:  "closed session",
			scope: bashPPNativeHandleScope{known: true},
			value: &bashPPBridgeValue{Kind: "handle", Type: "sync.WaitGroup", Session: "0123456789abcdef", Handle: 9},
			want:  errBashPPStaleNativeHandle,
		},
		{
			name:  "nested unbound",
			scope: liveNativeScope(),
			value: &bashPPBridgeValue{Kind: "struct", Fields: map[string]bashPPBridgeValue{
				"wg": {Kind: "handle", Type: "sync.WaitGroup", Handle: 9},
			}},
			want: errBashPPUnboundNativeHandle,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			cloner := newBashPPObjectCloner()
			cloner.native = test.scope
			_, err := cloner.clone(test.value)
			if !errors.Is(err, test.want) {
				t.Fatalf("clone error = %v, want %v", err, test.want)
			}
			// The handle is named, but its session id is an authority token
			// and must not appear in the message.
			if strings.Contains(err.Error(), "0123456789abcdef") {
				t.Fatalf("error leaked the session id: %v", err)
			}
		})
	}
}

// A snapshot taken before the dependency session has started must not guess
// that the handle is stale; the session itself validates ownership per request.
func TestBashPPTaskCloneAllowsUnknownNativeScope(t *testing.T) {
	cloner := newBashPPObjectCloner()
	value := &bashPPBridgeValue{Kind: "handle", Type: "sync.WaitGroup", Session: "0123456789abcdef", Handle: 5}
	cloned, err := cloner.clone(value)
	if err != nil {
		t.Fatal(err)
	}
	if got := cloned.(*bashPPBridgeValue); got.Session != value.Session || got.Handle != value.Handle {
		t.Fatalf("clone = %+v, want the same native object", got)
	}
}

// The regression itself, at the runner level: a task snapshot over an
// environment holding an imported native handle used to abort with
// "task snapshot: unsupported mutable Bash++ object type *interp.bashPPBridgeValue".
func TestBashPPTaskSnapshotAcceptsNativeHandle(t *testing.T) {
	var out strings.Builder // bashpp-racegate:safe-private
	r, err := New(Lang(syntax.LangBashPP), StdIO(nil, &out, &out))
	if err != nil {
		t.Fatal(err)
	}
	r.Reset()
	handle := &bashPPBridgeValue{Kind: "handle", Type: "sync.WaitGroup", Session: "0123456789abcdef", Handle: 11}
	r.bashPPTools.bridge = &bashPPNativeSession{}
	if err := r.writeEnv.Set("WG", expand.Variable{Set: true, Kind: expand.Object, Obj: handle}); err != nil {
		t.Fatal(err)
	}
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).
		Parse(strings.NewReader("func f() { return; }\ngo f()\n"), "handle.bpp")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), f); err != nil {
		t.Fatalf("run: %v out=%q", err, out.String())
	}
	if out.String() != "" {
		t.Fatalf("out = %q, want no snapshot failure", out.String())
	}
	if got := r.writeEnv.Get("WG").Obj; got != any(handle) {
		t.Fatalf("parent handle replaced by %+v", got)
	}
}

// Once the dependency session is closed, a handle still sitting in the
// environment must not reach a task.
func TestBashPPTaskSnapshotRefusesStaleNativeHandle(t *testing.T) {
	var out strings.Builder // bashpp-racegate:safe-private
	r, err := New(Lang(syntax.LangBashPP), StdIO(nil, &out, &out))
	if err != nil {
		t.Fatal(err)
	}
	r.Reset()
	handle := &bashPPBridgeValue{Kind: "handle", Type: "sync.WaitGroup", Session: "0123456789abcdef", Handle: 11}
	if err := r.writeEnv.Set("WG", expand.Variable{Set: true, Kind: expand.Object, Obj: handle}); err != nil {
		t.Fatal(err)
	}
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).
		Parse(strings.NewReader("func f() { return; }\ngo f()\n"), "stale.bpp")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), f); err == nil ||
		!strings.Contains(out.String(), "closed dependency session") {
		t.Fatalf("out=%q err=%v", out.String(), err)
	}
}
