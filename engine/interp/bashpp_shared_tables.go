// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp

import "maps"

// bashPPSharedTables records which of this runner's Bash++ program tables are
// still held by reference from another Runner rather than owned outright.
//
// WHY SHARING. A native testing callback frame is another Go execution frame
// in the registering task group, not a shell-copy boundary, so it starts from
// the registering runner's declarations. Deep-cloning them per frame made a
// loop of t.Run retain O(subtests × program) instead of O(subtests): the
// program is the large part, and the callback is the small one. See
// [Runner.subshellWithBashPPCapture].
//
// WHY COPY-ON-WRITE. The declarations are fixed before execution, but the
// tables holding them are not read-only: a Go-form body that declares a
// function-local type SHADOWS an entry in the flat type registry for the
// frame's lifetime ([Runner.bashPPShadowLocalType]), and one that binds a func
// literal APPENDS to the closure registry ([Runner.bashPPStoreFunc]). Parallel
// subtests run their bodies at the same time, so a table shared outright is a
// data race — and for the closure registry, whose backing array the frames
// share along with its spare capacity, two frames write one slot and each ends
// up naming the other's closure.
//
// So a shared table is private on first write. The flag is set on BOTH runners
// when a table is handed over, so whichever writes first takes the copy and
// the other keeps the original — an already-started parallel subtest does not
// observe a declaration the parent made after it was registered, which is what
// Go's compile-time program means anyway. A table nobody writes is never
// copied, which is what keeps the retention win for the read-only path.
//
// Every field is written only by the owning Runner's own goroutine, so the
// flags need no lock of their own; the point at which a table is published to
// a frame is likewise on the publishing runner's goroutine.
type bashPPSharedTables struct {
	types      bool
	funcs      bool
	methods    bool
	funcScopes bool
	closures   bool
}

// bashPPShareTables marks every program table as shared, for both sides of a
// hand-over. Callers must be running on the marked runner's own goroutine.
func (r *Runner) bashPPShareTables() {
	r.bashPPShared = bashPPSharedTables{
		types:      true,
		funcs:      true,
		methods:    true,
		funcScopes: true,
		closures:   true,
	}
}

// bashPPUnshareTypes takes private ownership of the named-type registry before
// a write to it. A nil registry has nothing to copy; the caller's own `make`
// then builds a map that is private by construction.
func (r *Runner) bashPPUnshareTypes() {
	if r.bashPPShared.types {
		r.bashPPShared.types = false
		r.bashPPTypes = maps.Clone(r.bashPPTypes)
	}
}

// bashPPUnshareFuncs takes private ownership of the Go-form function table.
// The *bashPPFunc values are not copied: a declaration and its body are fixed
// before execution, and a call binds its receiver and type arguments into its
// own frame rather than into the descriptor.
func (r *Runner) bashPPUnshareFuncs() {
	if r.bashPPShared.funcs {
		r.bashPPShared.funcs = false
		r.bashPPFuncs = maps.Clone(r.bashPPFuncs)
	}
}

// bashPPUnshareMethods takes private ownership of the method table. Both
// levels are copied: declaring a method on a type that already has one writes
// the inner map, not the outer.
func (r *Runner) bashPPUnshareMethods() {
	if !r.bashPPShared.methods {
		return
	}
	r.bashPPShared.methods = false
	if r.bashPPMethods == nil {
		return
	}
	private := make(map[string]map[string]*bashPPFunc, len(r.bashPPMethods))
	for typ, methods := range r.bashPPMethods {
		private[typ] = maps.Clone(methods)
	}
	r.bashPPMethods = private
}

// bashPPUnshareFuncScopes takes private ownership of the per-function lexical
// environments. The scopes themselves stay shared: a snapshot's cells are
// exactly what a function closed over, and the frame reaches them through the
// capture set it was authenticated with.
func (r *Runner) bashPPUnshareFuncScopes() {
	if r.bashPPShared.funcScopes {
		r.bashPPShared.funcScopes = false
		r.bashPPFuncScopes = maps.Clone(r.bashPPFuncScopes)
	}
}

// bashPPUnshareClosures takes private ownership of the closure registry.
//
// The copy is index for index and EXACT-LENGTH: a handle already held in a
// variable this frame inherited must keep naming the same closure, and leaving
// no spare capacity is what stops the very append this copy is made for from
// landing in the array a sibling frame is still appending to.
func (r *Runner) bashPPUnshareClosures() {
	if !r.bashPPShared.closures {
		return
	}
	r.bashPPShared.closures = false
	if len(r.bashPPClosures) == 0 {
		// Nothing to carry over, but drop any shared backing array so the
		// next append allocates rather than writing a sibling's slot.
		r.bashPPClosures = nil
		return
	}
	private := make([]*bashPPFunc, len(r.bashPPClosures))
	copy(private, r.bashPPClosures)
	r.bashPPClosures = private
}
