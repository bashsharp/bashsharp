package interp

import "github.com/bashsharp/bashsharp/engine/syntax"

// Sprint 165, lane frames-1: the line of a frame whose deferred calls are
// running.
//
// Go runs a function's deferred calls as the function returns, so a stack
// walk from inside a deferred call finds the deferring frame at its return
// point: the line of the `return` statement it executed, or the line of
// the body's closing brace when the body ran to its end. The interpreter
// records the position of the call that entered each frame, which for a
// deferred call is whatever statement ran last — the `defer` statement,
// or a statement of an earlier deferred call — so the frame table notes
// the return point itself, on the deferring frame, the moment its first
// deferred call is entered.
//
// That moment is visible at the push: a directly deferred call enters one
// frame deeper than the frame whose defers are running, which is the depth
// recover's rule keys on (bashPPDeferDepth). The return point is found
// without a record of the return statement: a return that ran leaves the
// return state active, and the statement executing is then the return
// itself — unless the return expression called an interpreted function,
// whose last statement is what the runner still points at; that statement
// lies outside the deferring body, and the position of the last call the
// frame made, which is the return statement's, stands in.

// goSourceFrameEntering notes, on the frame making a call, what the new
// frame tells about it: the position of the call, and — when the new frame
// is a directly deferred call — the point the frame is returning from.
func (r *Runner) goSourceFrameEntering() {
	if len(r.callStack) == 0 {
		return
	}
	caller := &r.callStack[len(r.callStack)-1]
	if r.bashPPDeferDepth == len(r.callStack)+1 && !caller.deferPos.IsValid() {
		caller.deferPos = r.goSourceReturnPos(caller)
	}
	caller.lastCallPos = r.curStmtPos
}

// goSourceReturnPos is the position a frame is returning from as its
// deferred calls begin.
func (r *Runner) goSourceReturnPos(frame *callFrame) syntax.Pos {
	var body *syntax.Block
	if frame.bashPPFn != nil {
		body = frame.bashPPFn.body()
	}
	if body == nil || !body.Rbrace.IsValid() {
		return r.curStmtPos
	}
	if r.bashPPReturn.active {
		if goSourceBlockContains(body, r.curStmtPos) {
			return r.curStmtPos
		}
		if goSourceBlockContains(body, frame.lastCallPos) {
			return frame.lastCallPos
		}
	}
	return body.Rbrace
}

// goSourceBlockContains reports whether pos lies within the block's braces.
func goSourceBlockContains(body *syntax.Block, pos syntax.Pos) bool {
	return pos.IsValid() && pos.Offset() >= body.Lbrace.Offset() && pos.Offset() <= body.Rbrace.Offset()
}

// goSourceFrameLinePos is the position frame i of the call stack is
// executing, given the frame above it: its return point while its deferred
// calls run, else the call that entered the frame above.
func (r *Runner) goSourceFrameLinePos(i int) syntax.Pos {
	if r.callStack[i].deferPos.IsValid() {
		return r.callStack[i].deferPos
	}
	return r.callStack[i+1].callPos
}
