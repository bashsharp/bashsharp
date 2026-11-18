//go:build full

package interp

// Sprint: #281; Story: #809; Story-ID: fac7e14af4a8

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func s281SameMap(t *testing.T, a, b any) bool {
	t.Helper()
	return reflect.ValueOf(a).Pointer() == reflect.ValueOf(b).Pointer()
}

// TestS281SharedProgramTablesCopyOnWrite pins the two halves of the callback
// frame table contract: a table a frame only READS stays the registering
// runner's one table however many frames exist, and a table a frame WRITES
// becomes private to that frame before the write lands, so neither the
// registering runner nor a sibling frame observes it.
func TestS281SharedProgramTablesCopyOnWrite(t *testing.T) {
	const programFuncs = 8
	parent, err := New(Lang(syntax.LangBashPP), Env(expand.ListEnviron()))
	if err != nil {
		t.Fatal(err)
	}
	defer parent.closeDirFile()
	parent.Reset()
	parent.bashPPGoSource = true
	parent.bashPPFileRun = true
	parent.bashPPConcurrent = parent.bashPPConcurrency(context.Background())
	defer parent.bashPPConcurrent.cancel()

	root := newBashPPScope(nil)
	parent.bashPPScope = root
	parent.bashPPFuncs = make(map[string]*bashPPFunc, programFuncs)
	parent.bashPPFuncScopes = make(map[string]*bashPPScope, programFuncs)
	parent.bashPPTypes = make(map[string]bashPPType, programFuncs)
	parent.bashPPMethods = map[string]map[string]*bashPPFunc{"Shared": {"Method": {}}}
	for i := range programFuncs {
		name := fmt.Sprintf("helper%d", i)
		parent.bashPPFuncs[name] = &bashPPFunc{lit: &syntax.BashPPFuncLit{Body: &syntax.Block{}}, scope: root}
		parent.bashPPFuncScopes[name] = root
		parent.bashPPTypes[fmt.Sprintf("Named%d", i)] = bashPPType{underlying: "struct"}
	}
	// Spare capacity is the ordinary state of a registry grown by append, and
	// it is what makes a shared closure registry lose entries rather than
	// merely be read from two frames.
	first, second := &bashPPFunc{}, &bashPPFunc{}
	parent.bashPPClosures = append(make([]*bashPPFunc, 0, 8), first, second)

	group := new(bashPPTestingCallbackFrames)
	newFrame := func() *Runner {
		t.Helper()
		fn := &bashPPFunc{lit: &syntax.BashPPFuncLit{Body: &syntax.Block{}}, scope: root}
		registered, err := parent.bashPPCallbackFunctionTemplate(fn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(registered.template.closeDirFile)
		frame, _, err := registered.bashPPTestingCallbackFrame(group)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(frame.closeDirFile)
		return frame
	}
	frameA, frameB := newFrame(), newFrame()

	// Registering a callback puts the registering runner on the same
	// copy-on-write footing, so its own later writes cannot reach a frame.
	if parent.bashPPShared != (bashPPSharedTables{types: true, funcs: true, methods: true, funcScopes: true, closures: true}) {
		t.Fatalf("registering runner kept exclusive tables: %+v", parent.bashPPShared)
	}
	for _, frame := range []*Runner{frameA, frameB} {
		if !s281SameMap(t, frame.bashPPFuncs, parent.bashPPFuncs) ||
			!s281SameMap(t, frame.bashPPTypes, parent.bashPPTypes) ||
			!s281SameMap(t, frame.bashPPMethods, parent.bashPPMethods) ||
			!s281SameMap(t, frame.bashPPFuncScopes, parent.bashPPFuncScopes) {
			t.Fatal("callback frame copied a program table it had not written")
		}
	}

	// A function-local type declaration.
	frameA.bashPPUnshareTypes()
	frameA.bashPPTypes["Local"] = bashPPType{underlying: "struct"}
	if _, leaked := parent.bashPPTypes["Local"]; leaked {
		t.Fatal("frame-local type reached the registering runner")
	}
	if _, leaked := frameB.bashPPTypes["Local"]; leaked {
		t.Fatal("frame-local type reached a sibling frame")
	}
	if len(frameA.bashPPTypes) != programFuncs+1 {
		t.Fatalf("private type registry lost entries: %d", len(frameA.bashPPTypes))
	}

	// A method declaration writes the INNER map of an existing entry, so the
	// copy has to reach both levels.
	frameA.bashPPUnshareMethods()
	frameA.bashPPMethods["Shared"]["Added"] = &bashPPFunc{}
	if _, leaked := parent.bashPPMethods["Shared"]["Added"]; leaked {
		t.Fatal("frame-local method reached the registering runner")
	}

	frameA.bashPPUnshareFuncs()
	frameA.bashPPFuncs["extra"] = &bashPPFunc{}
	frameA.bashPPUnshareFuncScopes()
	frameA.bashPPFuncScopes["extra"] = root
	if _, leaked := parent.bashPPFuncs["extra"]; leaked {
		t.Fatal("frame-local func reached the registering runner")
	}
	if _, leaked := parent.bashPPFuncScopes["extra"]; leaked {
		t.Fatal("frame-local func scope reached the registering runner")
	}

	// frameB wrote nothing, so it must still be sharing every table — this is
	// the retention win the sharing exists for.
	if !s281SameMap(t, frameB.bashPPFuncs, parent.bashPPFuncs) ||
		!s281SameMap(t, frameB.bashPPTypes, parent.bashPPTypes) ||
		!s281SameMap(t, frameB.bashPPMethods, parent.bashPPMethods) ||
		!s281SameMap(t, frameB.bashPPFuncScopes, parent.bashPPFuncScopes) {
		t.Fatal("a frame lost its sharing because a sibling wrote")
	}

	// The closure registry: both frames append at the same index.
	closureA, closureB := &bashPPFunc{}, &bashPPFunc{}
	handleA := frameA.bashPPStoreFunc(closureA).Str
	handleB := frameB.bashPPStoreFunc(closureB).Str
	if handleA != handleB {
		t.Fatalf("frames disagreed on the next handle: %q vs %q", handleA, handleB)
	}
	if got, ok := frameA.bashPPClosure(handleA); !ok || got != closureA {
		t.Fatalf("frame resolved its own handle to another frame's closure")
	}
	if got, ok := frameB.bashPPClosure(handleB); !ok || got != closureB {
		t.Fatalf("frame resolved its own handle to another frame's closure")
	}
	// Neither append may have touched the registry the frames started from,
	// including the slot beyond its length that they shared as spare capacity.
	if len(parent.bashPPClosures) != 2 || parent.bashPPClosures[0] != first || parent.bashPPClosures[1] != second {
		t.Fatalf("registering runner's closure registry changed: %v", parent.bashPPClosures)
	}
	if spare := parent.bashPPClosures[0:3:cap(parent.bashPPClosures)]; spare[2] != nil {
		t.Fatal("a frame appended into the registry's shared backing array")
	}

	for _, frame := range []*Runner{frameA, frameB} {
		group.finish(frame)
	}
}

// TestS281SharedProgramTablesUnshareIsIdempotent pins that taking ownership a
// second time is free: a frame that declares many local types must not copy
// the registry once per declaration.
func TestS281SharedProgramTablesUnshareIsIdempotent(t *testing.T) {
	r := &Runner{
		bashPPTypes:      map[string]bashPPType{"Named": {underlying: "struct"}},
		bashPPFuncs:      map[string]*bashPPFunc{"helper": {}},
		bashPPMethods:    map[string]map[string]*bashPPFunc{"Named": {"Method": {}}},
		bashPPFuncScopes: map[string]*bashPPScope{"helper": newBashPPScope(nil)},
		bashPPClosures:   append(make([]*bashPPFunc, 0, 4), &bashPPFunc{}),
	}
	r.bashPPShareTables()
	r.bashPPUnshareTypes()
	r.bashPPUnshareFuncs()
	r.bashPPUnshareMethods()
	r.bashPPUnshareFuncScopes()
	r.bashPPUnshareClosures()
	if r.bashPPShared != (bashPPSharedTables{}) {
		t.Fatalf("tables still marked shared after taking ownership: %+v", r.bashPPShared)
	}
	owned := [...]any{r.bashPPTypes, r.bashPPFuncs, r.bashPPMethods, r.bashPPFuncScopes}
	closures := r.bashPPClosures
	r.bashPPUnshareTypes()
	r.bashPPUnshareFuncs()
	r.bashPPUnshareMethods()
	r.bashPPUnshareFuncScopes()
	r.bashPPUnshareClosures()
	again := [...]any{r.bashPPTypes, r.bashPPFuncs, r.bashPPMethods, r.bashPPFuncScopes}
	for i := range owned {
		if !s281SameMap(t, owned[i], again[i]) {
			t.Fatalf("table %d was copied again although it was already owned", i)
		}
	}
	if len(closures) != len(r.bashPPClosures) || &closures[0] != &r.bashPPClosures[0] {
		t.Fatal("closure registry was copied again although it was already owned")
	}
}
