// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"fmt"
	"go/constant"
	"sync"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Lexical scopes for the Bash++ typed declarations.
//
// WHY A SECOND NAMESPACE AT ALL. A shell variable's lifetime is dynamic: it is
// created by an assignment, it outlives the compound command that created it,
// and a function sees its caller's locals. A Go declaration's lifetime is
// lexical: it begins at the declaration and ends with the enclosing block, and
// a function body sees only what was in scope where the function was written.
// The two rules disagree about `if true; then var x = 1; fi; echo $x`, so they
// cannot be the same namespace. Shell assignments keep bash's lifetime exactly;
// only names introduced by `var`/`const` live here.
//
// ONE NAMESPACE, MANY VIEWS. What must NOT differ is the VALUE a name has. A
// binding that expansion reads as 1 while an exec handler's Env reads as the
// shadowed shell value would be two variables wearing one name, and every
// consumer would have to know which one it got. So the scope chain is the
// single authority, and each read path is routed through it exactly once:
//
//   - expansion — [Runner.lookupVar], reached by expandEnv.Get
//   - name enumeration — expandEnv.Each, over [Runner.bashPPEnviron]
//   - HandlerCtx.Env — the same view, under the usual overlay
//   - external commands — execEnv over the same view
//   - writes — [Runner.setVar]
//   - deletes — [Runner.delVar]
//
// ISOLATION IS BY COPY, NOT BY LOCK. Subshells here are goroutines, not
// fork(), so a background subshell and its parent run concurrently in one
// address space. Sharing a cell pointer across that boundary is a data race
// the race detector reports, and sharing it is also wrong: bash gives a
// subshell a private copy of the whole variable space. [bashPPScope.clone]
// therefore deep-copies the chain, and [Runner.subshell] calls it for
// foreground and background subshells alike.

// bashPPCell is one binding's storage.
//
// It is a pointer target rather than a map value because a closure captures
// the CELL, not the value: a function defined inside a loop body must observe
// later mutations of the identifier it closed over, which is only possible if
// the snapshot and the live scope name the same cell.
type bashPPCell struct {
	vr expand.Variable
	// scalarKind retains whether an untyped rendered scalar came from a string,
	// bool, integer, or float expression. Re-parsing vr.Str would turn quoted
	// "2" into an integer and quoted "true" into a bool at assignment time.
	// exactScalar preserves untyped Go constants without a text round trip.
	exactScalar      constant.Value
	scalarKind       constant.Kind
	nonFinite        float64
	nonFiniteComplex complex128
	channel          *bashPPChannel
	channelOwner     *bashPPConcurrent
	// object is shared by every alias of one structured value. Deep readonly
	// is an attribute of this identity rather than of one variable spelling.
	object *bashPPObjectIdentity
	// valueMeta describes this cell's payload. It is separate from object so
	// struct/array copies can keep independent value layout while aliases still
	// share the readonly identity carried by object.
	valueMeta *bashPPCollectionMeta
	// typeName is non-empty for a value of a script-declared named type.
	// Pointer/nilPointer retain identity in-process; the visible shell value
	// remains vr, so typed values never need a lossy JSON representation.
	typeName       string
	declType       syntax.BashPPTypeExpr
	pointerValue   *bashPPPointer
	interfaceValue *bashPPInterfaceValue
	// unsafeAllocation is the byte image of this addressable allocation. It is
	// created lazily by an unsafe struct overlay. Ordinary typed storage stays
	// authoritative for declared fields; the image retains bytes (notably
	// padding) for which that storage has no carrier.
	unsafeAllocation *goSourceUnsafeAllocation
	// constant marks a `const` binding. It is kept beside vr.ReadOnly rather
	// than derived from it because the shell's readonly machinery is what
	// vr.ReadOnly drives, and the two answer to different owners: `declare -r`
	// may set the latter, but only `const` sets this.
	constant bool
	// The one-byte flags sit together so they share a word.
	negativeZero        bool
	hasNonFinite        bool
	hasNonFiniteComplex bool
	pointer             bool
	nilPointer          bool
	// guard is non-nil once this cell has been aliased into an interpreted
	// goroutine's snapshot, so that two host goroutines can reach its fields.
	// It is a pointer, not an embedded mutex, because cells are copied by
	// value here and in bashpp_send/bashpp_generic_body. See
	// bashpp_cell_share.go for why an aliased cell needs a lock at all.
	guard *sync.RWMutex
}

func (c *bashPPCell) vrValue() any {
	if c == nil {
		return ""
	}
	// The kind test and the payload it selects must see the same store, so a
	// shared cell answers from one snapshot; see bashpp_cell_share.go.
	vr := c.viewVar()
	if vr.Kind == expand.Object {
		return vr.Obj
	}
	return vr.String()
}

// bashPPScope is one lexical block's declarations, linked to its enclosing
// block. Lookup walks outwards; declaration only ever writes to the innermost.
type bashPPScope struct {
	parent  *bashPPScope
	entries map[string]*bashPPCell
	// linknames is populated only on the outermost Go-source scope. It maps
	// linker package symbols to the same cells entries exposes by source name;
	// keeping it out of entries prevents synthetic linker keys from leaking
	// through environment/name enumeration.
	linknames map[string]*bashPPCell
	// labels records, per Go label executed in this block, which names the
	// block had declared when the label was reached. A backward goto to the
	// label re-enters the block at that point, so the names declared after
	// it go out of scope again and their declarations run afresh.
	labels map[string]map[string]bool
}

func newBashPPScope(parent *bashPPScope) *bashPPScope {
	return &bashPPScope{parent: parent, entries: make(map[string]*bashPPCell)}
}

// newBashPPBlockScope is the scope [Runner.bashPPPushScope] enters. Its entry
// map is allocated by the first declaration: most blocks a loop body enters
// declare nothing, and a nil map reads, ranges and deletes as an empty one.
// Every write to a scope's entries therefore goes through [bashPPScope.put].
func newBashPPBlockScope(parent *bashPPScope) *bashPPScope {
	return &bashPPScope{parent: parent}
}

// put binds name to cell in THIS block, replacing any binding it had.
func (s *bashPPScope) put(name string, cell *bashPPCell) {
	if s.entries == nil {
		s.entries = make(map[string]*bashPPCell)
	}
	s.entries[name] = cell
}

// lookup finds the innermost binding of name, or nil.
func (s *bashPPScope) lookup(name string) *bashPPCell {
	for at := s; at != nil; at = at.parent {
		// Most blocks between a loop body and its function declare nothing.
		if len(at.entries) == 0 {
			continue
		}
		if cell, ok := at.entries[name]; ok {
			return cell
		}
	}
	return nil
}

// declare introduces name in THIS block.
//
// Redeclaration in the same block is an error, as it is in Go; redeclaration
// in an inner block is ordinary shadowing and is allowed. This is what makes
// "the loop body got a fresh scope" observable from a script: a second
// iteration re-running `var i = 1` succeeds precisely because the first
// iteration's block is gone.
func (s *bashPPScope) declare(name string, vr expand.Variable, constant bool) error {
	// The blank identifier binds nothing a program can name, so `var _ = x`
	// may be spelled any number of times in one block: each spelling
	// replaces the discarded cell rather than clashing with it.
	if _, exists := s.entries[name]; exists && name != "_" {
		return fmt.Errorf("%s redeclared in this block", name)
	}
	vr.ReadOnly = vr.ReadOnly || constant
	s.put(name, &bashPPCell{vr: vr, constant: constant})
	return nil
}

// markLabel records the names declared in this block when label is reached.
func (s *bashPPScope) markLabel(label string) {
	if s.labels == nil {
		s.labels = make(map[string]map[string]bool)
	}
	visible := make(map[string]bool, len(s.entries))
	for name := range s.entries {
		visible[name] = true
	}
	s.labels[label] = visible
}

// resumeAtLabel drops the names this block declared after label was reached,
// so that re-running the statements from the label declares them anew as Go
// does. A label the block has not reached yet (a forward goto) declared
// nothing to drop: Go rejects a goto that would skip a declaration.
func (s *bashPPScope) resumeAtLabel(label string) {
	visible, ok := s.labels[label]
	if !ok {
		return
	}
	for name := range s.entries {
		if !visible[name] {
			delete(s.entries, name)
		}
	}
}

// snapshot freezes which NAMES are visible while keeping the cells live.
//
// This is the whole of Go's "the scope of a declared identifier begins at the
// declaration" rule, and it is why the capture cannot simply be a pointer to
// the enclosing scope. A function defined before `var x = 1` runs in a block
// that does not contain x, so it must not see x — but a function defined after
// `var i = 1` and called after `i` was reassigned must see the NEW value.
// Copying the entry maps satisfies the first (later declarations land in the
// original map, not the copy) and sharing the cells satisfies the second.
func (s *bashPPScope) snapshot() *bashPPScope {
	if s == nil {
		return nil
	}
	out := newBashPPScope(s.parent.snapshot())
	for name, cell := range s.entries {
		out.put(name, cell)
	}
	return out
}

// clone deep-copies the chain so a subshell shares nothing with its parent.
//
// The memo is not an optimization. A runner's live scope and the scopes it
// captured for its functions overlap: a closure's chain typically shares
// blocks, and after [bashPPScope.snapshot] it shares individual cells. Cloning
// each reachable scope independently would silently split those aliases, so a
// function that observed a variable before the subshell would stop observing
// it inside one. Memoizing on the ORIGINAL pointer reproduces the aliasing
// graph exactly, one edge at a time.
type bashPPCloner struct {
	scopes   map[*bashPPScope]*bashPPScope
	cells    map[*bashPPCell]*bashPPCell
	objects  map[*bashPPObjectIdentity]*bashPPObjectIdentity
	values   *bashPPObjectCloner
	metas    map[*bashPPCollectionMeta]*bashPPCollectionMeta
	pointers map[*bashPPPointer]*bashPPPointer
	// shared are cells to alias rather than copy, because an original Go
	// closure captures them by reference; see gosource_task_capture.go. It is
	// nil for every classic Bash++ clone, so the deep copy is unchanged there.
	shared       map[*bashPPCell]bool
	goSourceTask bool
	// exactCapture is used by native callback frames whose lexical capture was
	// proved before the dependency scheduler took ownership. Cells outside that
	// set are neither readable nor writable by the callback, so retaining them
	// would turn every callback into an O(program) snapshot.
	exactCapture bool
}

func newBashPPCloner() *bashPPCloner { return newBashPPClonerFor(nil) }

// newBashPPClonerFor is [newBashPPCloner] with the dependency session the
// clone will run against, so imported native handles are checked against it;
// see bashpp_task.go. A nil runner keeps handle identity without that check.
func newBashPPClonerFor(r *Runner) *bashPPCloner {
	c := &bashPPCloner{
		scopes:   make(map[*bashPPScope]*bashPPScope),
		cells:    make(map[*bashPPCell]*bashPPCell),
		objects:  make(map[*bashPPObjectIdentity]*bashPPObjectIdentity),
		values:   newBashPPObjectCloner(),
		metas:    make(map[*bashPPCollectionMeta]*bashPPCollectionMeta),
		pointers: make(map[*bashPPPointer]*bashPPPointer),
	}
	c.values.pointer = c.clonePointer
	c.values.native = bashPPNativeScopeOf(r)
	return c
}

func (c *bashPPCloner) clonePointer(pointer *bashPPPointer) *bashPPPointer {
	if pointer == nil {
		return nil
	}
	if done := c.pointers[pointer]; done != nil {
		return done
	}
	copy := *pointer
	out := &copy
	c.pointers[pointer] = out
	out.path = append([]bashPPPointerStep(nil), pointer.path...)
	out.target = c.cloneCell(pointer.target)
	return out
}

func (c *bashPPCloner) clone(s *bashPPScope) *bashPPScope {
	if s == nil {
		return nil
	}
	if done, ok := c.scopes[s]; ok {
		return done
	}
	out := &bashPPScope{entries: make(map[string]*bashPPCell, len(s.entries))}
	// Record before recursing: the chain is acyclic today, but memoizing
	// first is what keeps that from being a precondition.
	c.scopes[s] = out
	out.parent = c.clone(s.parent)
	for name, cell := range s.entries {
		if cloned := c.cloneCell(cell); cloned != nil || !c.exactCapture {
			out.entries[name] = cloned
		}
	}
	if len(s.linknames) > 0 {
		out.linknames = make(map[string]*bashPPCell, len(s.linknames))
		for name, cell := range s.linknames {
			out.linknames[name] = c.cloneCell(cell)
		}
	}
	return out
}

func (c *bashPPCloner) cloneCell(cell *bashPPCell) *bashPPCell {
	if cell == nil {
		return nil
	}
	if copied, ok := c.cells[cell]; ok {
		return copied
	}
	if c.shared[cell] {
		// GoSource lexical capture: the task and its parent must name one
		// variable. Memoize the identity so every other edge which reaches
		// this cell — an alias, a closure's captured scope, a pointer target —
		// lands on the same shared cell rather than forking a copy.
		//
		// This is the one place a cell stops being owned by a single host
		// goroutine, so it is also where its guard is armed; see
		// bashpp_cell_share.go.
		cell.shareGuard()
		c.cells[cell] = cell
		return cell
	}
	if c.exactCapture {
		// Callback capture analysis authenticated every cell the body can
		// reach. Do not copy the rest of the linked program into each testing
		// callback frame.
		c.cells[cell] = nil
		return nil
	}
	// An ALREADY-shared cell reached again through an unrelated edge is live
	// under another goroutine's writes, so even this one marker is read through
	// the guard; see bashpp_cell_share.go.
	if c.goSourceTask && !cell.viewConstant() {
		// Exact Go lexical capture has already selected every outer binding
		// a carried original callable can access. Omit unrelated locals:
		// reading even their scalar slots can race with a task using &local.
		// Actual argument values are prepared separately before this clone.
		c.cells[cell] = nil
		return nil
	}
	// A deep copy is private to the cloning goroutine, so it is read as one
	// snapshot and starts unaliased rather than contending on the guard of the
	// cell it was copied from; view() does both.
	dup := *cell.view()
	copied := &dup
	// Publish before following pointer edges, which may lead back here.
	c.cells[cell] = copied
	dup.vr = cloneBashPPVariable(cell.vr)
	if cell.vr.Kind == expand.Object && cell.vr.Obj != nil {
		if value, err := c.values.clone(cell.vr.Obj); err == nil {
			dup.vr.Obj = value
		}
	}
	if cell.object != nil {
		if object := c.objects[cell.object]; object != nil {
			dup.object = object
		} else {
			objectCopy := *cell.object
			dup.object = &objectCopy
			c.objects[cell.object] = dup.object
		}
	}
	dup.valueMeta = bashPPCloneCollectionMeta(cell.valueMeta, c.metas, c.cloneCell)
	if dup.object != nil {
		dup.object.collection = bashPPCloneCollectionMeta(cell.object.collection, c.metas, c.cloneCell)
	}
	if cell.pointerValue != nil {
		dup.pointerValue = c.clonePointer(cell.pointerValue)
	}
	if cell.interfaceValue != nil {
		iface := *cell.interfaceValue
		iface.cell = c.cloneCell(cell.interfaceValue.cell)
		dup.interfaceValue = &iface
	}
	dup.unsafeAllocation = cell.unsafeAllocation.clone(c.clonePointer)
	return copied
}

func cloneBashPPVariable(vr expand.Variable) expand.Variable {
	vr.List = append([]string(nil), vr.List...)
	if vr.ListMap != nil {
		src := vr.ListMap
		vr.ListMap = make(map[int]string, len(src))
		for key, value := range src {
			vr.ListMap[key] = value
		}
	}
	if vr.ListSet != nil {
		src := vr.ListSet
		vr.ListSet = make(map[int]bool, len(src))
		for key, value := range src {
			vr.ListSet[key] = value
		}
	}
	if vr.Map != nil {
		src := vr.Map
		vr.Map = make(map[string]string, len(src))
		for key, value := range src {
			vr.Map[key] = value
		}
	}
	return vr
}

// bashPPPushScope enters a new lexical block, returning the function that
// leaves it. Callers defer the result; the pair must bracket the block's
// statements and nothing else.
func (r *Runner) bashPPPushScope() func() {
	previous := r.bashPPScope
	r.bashPPScope = newBashPPBlockScope(previous)
	return func() { r.bashPPScope = previous }
}

// bashPPEnviron presents the scope chain and the shell's variables as one
// environment, so that every consumer which enumerates variables — HandlerCtx,
// execEnv, `${!prefix@}` — sees the same values expansion does.
//
// Get prefers the innermost typed binding; Each yields the shell's variables
// first and the typed bindings last, which is what [expand.Environ] asks for
// when a name appears twice.
type bashPPEnviron struct {
	scope  *bashPPScope
	parent expand.Environ
}

var _ expand.Environ = bashPPEnviron{}

func (e bashPPEnviron) Get(name string) expand.Variable {
	if cell := e.scope.lookup(name); cell != nil {
		// Every expansion of a `var` binding comes through here, including one
		// evaluated in an interpreted task that names a cell its launcher also
		// holds, so the value is read through its guard.
		return cell.viewVar()
	}
	return e.parent.Get(name)
}

func (e bashPPEnviron) Each(fn func(name string, vr expand.Variable) bool) {
	// Each shadowed name is yielded ONCE, with the typed value. [expand.Environ]
	// permits a repeated name and says the last one wins, but execEnv turns its
	// result into a child's environment block, and there the two entries would
	// both survive into execve — leaving which value the child reads up to
	// whichever end of the array its libc happens to scan from. Collecting the
	// visible bindings first is what keeps that from being a portability
	// question.
	visible := e.scope.visible()
	for name, vr := range e.parent.Each {
		if _, shadowed := visible[name]; shadowed {
			continue
		}
		if !fn(name, vr) {
			return
		}
	}
	for name, vr := range visible {
		if !fn(name, vr) {
			return
		}
	}
}

// visible flattens the chain into the bindings a lookup would find, with an
// inner declaration replacing the outer one it shadows.
func (s *bashPPScope) visible() map[string]expand.Variable {
	if s == nil {
		return nil
	}
	out := s.parent.visible()
	if out == nil {
		out = make(map[string]expand.Variable, len(s.entries))
	}
	for name, cell := range s.entries {
		out[name] = cell.vr
	}
	return out
}

// bashPPEnv returns the environment view every enumeration path must use. It
// is r.writeEnv itself whenever no typed binding can exist, so LangBash pays
// nothing for this.
func (r *Runner) bashPPEnv() expand.Environ {
	if r.bashPPScope == nil {
		return r.writeEnv
	}
	return bashPPEnviron{scope: r.bashPPScope, parent: r.writeEnv}
}
