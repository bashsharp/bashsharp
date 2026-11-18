package shellrt

import (
	"fmt"
	"go/constant"
	"reflect"
	"sort"
	"sync"
)

// LexicalCell is native addressable storage for one source binding. Presence
// starts at its executed declaration, independently of the Go zero value.
// Cells belong to one sequential execution. Native reads and writes need no
// runtime indirection until a shell write establishes raw provenance; those
// reads must use Load/LoadAddress. Concurrent mutation requires the same
// synchronization as an ordinary Go variable.
type LexicalCell[T any] struct {
	Value   T
	Present bool
}

type lexicalSlot struct {
	cell        any
	value       reflect.Value
	present     *bool
	kind        Kind
	name        string
	raw         *lexicalRaw
	forkPending bool
}

type lexicalStore struct {
	mu    sync.Mutex
	slots map[string]*lexicalSlot
}

// LexicalBindings separates stable cell identity from a captured set of source
// names. Each entry owns a new store; captured views share only that entry's
// cells by pointer. Capturing copies the ID registry as well as name visibility,
// so re-registering an activation cannot retarget an older capture. Registry
// operations are synchronized, but do not synchronize native
// access to Cell.Value or its reachable object graph.
type LexicalBindings struct {
	addresses *lexicalAddresses
	pending   map[lexicalAddress]lexicalPending
	store     *lexicalStore
	mu        sync.RWMutex
	names     map[string]string
}

func NewLexicalBindings() *LexicalBindings {
	return &LexicalBindings{addresses: &lexicalAddresses{slots: map[lexicalAddress]*lexicalSlot{}}, store: &lexicalStore{slots: map[string]*lexicalSlot{}}, names: map[string]string{}}
}

// Cell returns stable storage by a compiler-assigned hygienic ID and makes its
// source name visible in this view. The compiler must use the same T and kind
// for every occurrence of an ID. Allocation does not establish presence.
func Cell[T any](bindings *LexicalBindings, id, name string, kind Kind) *LexicalCell[T] {
	bindings.store.mu.Lock()
	slot := bindings.store.slots[id]
	if slot == nil {
		cell := &LexicalCell[T]{}
		slot = &lexicalSlot{cell: cell, value: reflect.ValueOf(&cell.Value).Elem(), present: &cell.Present, kind: kind, name: name, raw: &lexicalRaw{}}
		bindings.store.slots[id] = slot
		bindings.track(slot)
	}
	cell, ok := slot.cell.(*LexicalCell[T])
	bindings.store.mu.Unlock()
	if !ok || slot.kind != kind {
		panic("shellrt: inconsistent lexical binding type or projection for " + id)
	}
	bindings.mu.Lock()
	bindings.names[name] = id
	bindings.mu.Unlock()
	return cell
}

// CaptureNames freezes exactly the supplied source-name-to-ID mapping. Later
// declarations in the parent view remain invisible, while writes to cells
// already captured remain visible. The caller's map is never retained.
func (b *LexicalBindings) CaptureNames(names map[string]string) *LexicalBindings {
	view := NewLexicalBindings()
	view.addresses = b.addresses
	view.pending = b.pending
	b.store.mu.Lock()
	defer b.store.mu.Unlock()
	for name, id := range names {
		view.names[name] = id
		if slot := b.store.slots[id]; slot != nil {
			view.store.slots[id] = slot
		}
	}
	return view
}

// Register exposes existing native storage without copying it. This preserves
// the addresses of ordinary parameters, named results and per-iteration locals.
// Re-registering an ID for a new activation affects this view only; already
// captured views keep the earlier storage. An ID's T and kind must stay fixed.
// Cell-owned and externally registered storage must not be mixed for one ID.
func Register[T any](b *LexicalBindings, id, name string, value *T, present *bool, kind Kind) error {
	if value == nil || present == nil {
		return &LexicalWriteError{name, "registration requires non-nil storage addresses"}
	}
	b.store.mu.Lock()
	existing := b.store.slots[id]
	if existing != nil && (existing.value.Type() != reflect.TypeFor[T]() || existing.kind != kind) {
		b.store.mu.Unlock()
		return &LexicalWriteError{name, "inconsistent binding type or projection"}
	}
	if existing != nil && existing.cell != nil {
		b.store.mu.Unlock()
		return &LexicalWriteError{name, "cannot replace cell-owned storage with external storage"}
	}
	raw := &lexicalRaw{}
	if old := b.slotAt(value); old != nil {
		raw = old.raw
	}
	if existing != nil && existing.forkPending {
		raw = existing.raw
	}
	// Multiple IDs exposing the same real native address share its spelling.
	for _, slot := range b.store.slots {
		if slot.value.Type() == reflect.TypeFor[T]() && slot.value.Addr().Pointer() == reflect.ValueOf(value).Pointer() {
			raw = slot.raw
			break
		}
	}
	b.store.slots[id] = &lexicalSlot{value: reflect.ValueOf(value).Elem(), present: present, kind: kind, name: name, raw: raw}
	b.track(b.store.slots[id])
	b.store.mu.Unlock()
	b.mu.Lock()
	b.names[name] = id
	b.mu.Unlock()
	return nil
}

// Fork makes independent, absent zero cells with the same identity metadata.
// It does not copy native values or graphs. Before running the child, generated
// code must register every parent/child Value address with the SAME Snapshot
// as its other roots, copy Present, and install the snapshot's readonly state.
// Captured child views must be created from this fork, never the parent's store.
func (b *LexicalBindings) Fork() *LexicalBindings {
	child := NewLexicalBindings()
	b.mu.RLock()
	for name, id := range b.names {
		child.names[name] = id
	}
	b.mu.RUnlock()
	b.store.mu.Lock()
	defer b.store.mu.Unlock()
	rawCopies := map[*lexicalRaw]*lexicalRaw{}
	for id, slot := range b.store.slots {
		raw := rawCopies[slot.raw]
		if raw == nil {
			copy := *slot.raw
			copy.value = slot.raw.value.clone()
			raw = &copy
			rawCopies[slot.raw] = raw
		}
		// Constants have no native address to register with Snapshot. Their
		// immutable scalar value and presence cross the boundary directly;
		// the shadow remains unaddressable through the provenance registry.
		if slot.raw.info.Constant {
			value := reflect.New(slot.value.Type()).Elem()
			value.Set(slot.value)
			present := *slot.present
			child.store.slots[id] = &lexicalSlot{value: value, present: &present, kind: slot.kind, name: slot.name, raw: raw}
			continue
		}
		if slot.cell == nil {
			child.store.slots[id] = &lexicalSlot{value: reflect.New(slot.value.Type()).Elem(), present: new(bool), kind: slot.kind, name: slot.name, raw: raw, forkPending: true}
			child.track(child.store.slots[id])
			continue
		}
		cell := reflect.New(reflect.TypeOf(slot.cell).Elem())
		child.store.slots[id] = &lexicalSlot{cell: cell.Interface(), value: cell.Elem().FieldByName("Value"), present: cell.Elem().FieldByName("Present").Addr().Interface().(*bool), kind: slot.kind, name: slot.name, raw: raw, forkPending: true}
		child.track(child.store.slots[id])
	}
	child.pending = map[lexicalAddress]lexicalPending{}
	b.addresses.mu.Lock()
	for address, slot := range b.addresses.slots {
		raw := rawCopies[slot.raw]
		if raw == nil {
			copy := *slot.raw
			copy.value = slot.raw.value.clone()
			raw = &copy
			rawCopies[slot.raw] = raw
		}
		child.pending[address] = lexicalPending{slot.name, slot.kind, *slot.present, raw}
	}
	b.addresses.mu.Unlock()
	return child
}

func (b *LexicalBindings) visible() map[string]*lexicalSlot {
	names := map[string]string{}
	b.mu.RLock()
	for name, id := range b.names {
		names[name] = id
	}
	b.mu.RUnlock()
	slots := map[string]*lexicalSlot{}
	b.store.mu.Lock()
	defer b.store.mu.Unlock()
	for name, id := range names {
		if slot := b.store.slots[id]; slot != nil {
			slots[name] = slot
		}
	}
	return slots
}

// ShellValue uses the captured typed binding when present, otherwise ordinary
// shell lookup. It never infers a rich value from serialized shell text.
func (b *LexicalBindings) ShellValue(session *Session, name string) (string, bool, error) {
	if slot := b.visible()[name]; slot != nil && *slot.present {
		value, err := slot.shellText()
		return value, true, err
	}
	value, ok := session.Get(name)
	return value.String(), ok, nil
}

// LexicalWriteError marks a shell write which this native cell cannot represent.
// It is an explicit lowering boundary, not a rewritten interpreter diagnostic.
// Generated callers own reporting and control transfer.
type LexicalWriteError struct{ Name, Message string }

func (e *LexicalWriteError) Error() string {
	return fmt.Sprintf("shellrt: lexical binding %s: %s", e.Name, e.Message)
}
func (*LexicalWriteError) ExitStatus() int { return 2 }

type lexicalOverlay struct {
	name      string
	slot      *lexicalSlot
	original  Var
	existed   bool
	projected Var
}

// LexicalExchange is a sequential, one-use overlay. EndShell must run even when
// the shell region fails. Nested exchanges must be ended in reverse order.
type LexicalExchange struct {
	session  *Session
	overlays []lexicalOverlay
	ended    bool
}

// BeginShell projects only visible present typed bindings. All projections are
// prepared before changing the session. Ordinary variable attributes survive
// the overlay, and unrelated shell names are never restored or discarded.
func (b *LexicalBindings) BeginShell(session *Session) (*LexicalExchange, error) {
	slots := b.visible()
	names := make([]string, 0, len(slots))
	for name := range slots {
		names = append(names, name)
	}
	sort.Strings(names)
	exchange := &LexicalExchange{session: session}
	for _, name := range names {
		slot := slots[name]
		if !*slot.present {
			continue
		}
		text, err := slot.shellText()
		if err != nil {
			return nil, err
		}
		exchange.overlays = append(exchange.overlays, lexicalOverlay{name: name, slot: slot, projected: Var{Str: text}})
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	for i := range exchange.overlays {
		overlay := &exchange.overlays[i]
		original, ok := session.state.Vars[overlay.name]
		overlay.original, overlay.existed = original.clone(), ok
		overlay.projected.Exported, overlay.projected.ReadOnly = original.Exported, original.ReadOnly
		session.state.Vars[overlay.name] = overlay.projected.clone()
	}
	return exchange, nil
}

// EndShell stages supported shell writes, retaining raw scalar spelling and
// deferring numeric conversion errors until a typed read.
// The original shell variables are restored on success AND failure so a later
// typed declaration cannot leak into a closure's earlier captured name view.
// An unsupported rich mutation never overwrites its native object with JSON.
func (e *LexicalExchange) EndShell(session *Session) error {
	if e == nil || e.ended || e.session != session {
		return &LexicalWriteError{Message: "invalid or already ended shell exchange"}
	}
	e.ended = true
	session.mu.Lock()
	defer session.mu.Unlock()
	defer func() {
		for _, overlay := range e.overlays {
			if overlay.existed {
				session.state.Vars[overlay.name] = overlay.original.clone()
			} else {
				delete(session.state.Vars, overlay.name)
			}
		}
	}()
	type write struct {
		slot    *lexicalSlot
		value   reflect.Value
		present bool
		raw     Var
		scalar  constant.Value
	}
	var pending []write
	for _, overlay := range e.overlays {
		actual, present := session.state.Vars[overlay.name]
		if present && actual.Equal(overlay.projected) {
			continue
		}
		if !present {
			return &LexicalWriteError{overlay.name, "unsetting a typed declaration requires source statement handling"}
		}
		// Attribute-only writes do not alter native value identity. Attribute
		// ownership/readonly provenance remains the compiler's explicit contract.
		if actual.Kind == overlay.projected.Kind && actual.String() == overlay.projected.String() {
			continue
		}
		if overlay.slot.kind != KindScalar {
			return &LexicalWriteError{overlay.name, "writes to a rich projection require an explicit native conversion"}
		}
		if actual.Kind != Scalar {
			return &LexicalWriteError{overlay.name, "array writes require an explicit native conversion"}
		}
		scalar := lexicalRawScalar(overlay.slot.value.Type(), actual.Str)
		value, err := lexicalNativeScalar(overlay.slot, scalar, ValueSite{Name: overlay.name})
		// Conversion failure belongs to the later typed read. Clear physical
		// storage so no previous native value can accidentally survive it.
		if err != nil {
			value = reflect.Zero(overlay.slot.value.Type())
		}
		pending = append(pending, write{overlay.slot, value, true, actual.clone(), scalar})
	}
	for _, write := range pending {
		write.slot.value.Set(write.value)
		*write.slot.present = write.present
		write.slot.raw.value, write.slot.raw.scalar, write.slot.raw.present = write.raw, write.scalar, true
		write.slot.raw.nativeScalar = nil
	}
	return nil
}

func lexicalScalarWrite(name string, typ reflect.Type, value Var) (reflect.Value, error) {
	if value.Kind != Scalar {
		return reflect.Value{}, &LexicalWriteError{name, "array writes require an explicit native conversion"}
	}
	slot := &lexicalSlot{name: name, value: reflect.New(typ).Elem(), raw: &lexicalRaw{}}
	return lexicalNativeScalar(slot, lexicalRawScalar(typ, value.Str), ValueSite{Name: name})
}
