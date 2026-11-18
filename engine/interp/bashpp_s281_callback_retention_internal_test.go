//go:build full

package interp

// Sprint: #281; Story: #809; Story-ID: fac7e14af4a8

import (
	"context"
	"fmt"
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// TestS281CallbackSubtestCloneRetentionBounded models the part of testing.Run
// which used to exhaust the testdir host: each subtest registers a distinct
// interpreted closure and receives an independently schedulable callback
// frame. The program declarations are deliberately much larger than the
// callbacks. Retention should therefore be one shared immutable program plus
// O(subtests), not a complete program clone per registration and frame.
func TestS281CallbackSubtestCloneRetentionBounded(t *testing.T) {
	const (
		programFuncs = 96
		subtests     = 24
	)
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
	for i := range programFuncs {
		root.entries[fmt.Sprintf("constant%d", i)] = &bashPPCell{
			vr:       expand.Variable{Set: true, Kind: expand.String, Str: fmt.Sprint(i)},
			constant: true,
		}
		parent.bashPPFuncs[fmt.Sprintf("helper%d", i)] = &bashPPFunc{
			lit:   &syntax.BashPPFuncLit{Body: &syntax.Block{}},
			scope: root,
		}
	}

	type retainedCallback struct {
		registered *bashPPCallbackFunction
		frame      *Runner
		frameFn    *bashPPFunc
	}
	retained := make([]retainedCallback, 0, subtests)
	group := new(bashPPTestingCallbackFrames)
	for range subtests {
		fn := &bashPPFunc{lit: &syntax.BashPPFuncLit{Body: &syntax.Block{}}, scope: root}
		registered, err := parent.bashPPCallbackFunctionTemplate(fn)
		if err != nil {
			t.Fatal(err)
		}
		frame, frameFn, err := registered.bashPPTestingCallbackFrame(group)
		if err != nil {
			t.Fatal(err)
		}
		group.add(frame)
		retained = append(retained, retainedCallback{registered, frame, frameFn})
	}
	defer func() {
		for _, callback := range retained {
			callback.registered.template.closeDirFile()
			callback.frame.closeDirFile()
		}
	}()

	funcs := make(map[*bashPPFunc]bool)
	scopes := make(map[*bashPPScope]bool)
	cells := make(map[*bashPPCell]bool)
	var visitScope func(*bashPPScope)
	visitScope = func(scope *bashPPScope) {
		if scope == nil || scopes[scope] {
			return
		}
		scopes[scope] = true
		visitScope(scope.parent)
		for _, cell := range scope.entries {
			if cell != nil {
				cells[cell] = true
			}
		}
	}
	visitFunc := func(fn *bashPPFunc) {
		if fn == nil || funcs[fn] {
			return
		}
		funcs[fn] = true
		visitScope(fn.scope)
		if fn.receiver != nil {
			cells[fn.receiver] = true
		}
	}
	visitRunner := func(r *Runner) {
		visitScope(r.bashPPScope)
		for _, fn := range r.bashPPFuncs {
			visitFunc(fn)
		}
		for _, methods := range r.bashPPMethods {
			for _, fn := range methods {
				visitFunc(fn)
			}
		}
		for _, fn := range r.bashPPClosures {
			visitFunc(fn)
		}
	}
	for _, callback := range retained {
		visitRunner(callback.registered.template)
		visitFunc(callback.registered.templateFn)
		visitRunner(callback.frame)
		visitFunc(callback.frameFn)
	}

	maxFuncs := programFuncs + 2*subtests
	maxCells := programFuncs + 2*subtests
	if len(funcs) > maxFuncs || len(cells) > maxCells {
		t.Fatalf("%d callback subtests retained %d funcs and %d cells; want at most %d funcs and %d cells",
			subtests, len(funcs), len(cells), maxFuncs, maxCells)
	}
	t.Logf("%d callback subtests retained %d unique funcs and %d unique cells", subtests, len(funcs), len(cells))
	for _, callback := range retained {
		group.finish(callback.frame)
		group.Done()
	}
	group.closeFrames()
	if len(group.frames) != 0 {
		t.Fatalf("completed callback frames retained %d runners", len(group.frames))
	}
}

func TestS281SynchronousCallbackRegistrationReleased(t *testing.T) {
	template := &Runner{}
	registered := &bashPPCallbackFunction{template: template}
	session := &bashPPNativeSession{
		functions:      map[uint64]*bashPPFunc{7: {}},
		functionOwners: map[uint64]*bashPPCallbackFunction{7: registered},
	}
	request := bashPPBridgeRequest{Args: []bashPPBridgeValue{{
		Kind:        "callback",
		Handle:      7,
		newCallback: true,
	}}}
	session.releaseCallbackFunctions(bashPPNewCallbackHandles(request))
	if len(session.functions) != 0 || len(session.functionOwners) != 0 {
		t.Fatalf("completed synchronous callback retained functions=%d owners=%d", len(session.functions), len(session.functionOwners))
	}
}
