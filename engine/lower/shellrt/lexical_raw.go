package shellrt

import (
	"fmt"
	"go/constant"
	"go/token"
	"math"
	"math/big"
	"reflect"
	"sync"
)

// LexicalInfo records source spelling and declaration attributes independently
// from native T. Backend enforcement of const/unset/attribute events remains
// separate from deferred typed conversion at a read.
type LexicalInfo struct {
	SourceType                   string
	Constant, Exported, Readonly bool
}

type lexicalRaw struct {
	info         LexicalInfo
	value        Var
	scalar       constant.Value
	nativeScalar constant.Value
	present      bool
}

func (b *LexicalBindings) slot(id string) *lexicalSlot {
	b.store.mu.Lock()
	defer b.store.mu.Unlock()
	return b.store.slots[id]
}

func (b *LexicalBindings) SetInfo(id string, info LexicalInfo) error {
	slot := b.slot(id)
	if slot == nil {
		return &LexicalWriteError{id, "unknown binding ID"}
	}
	slot.raw.info = info
	return nil
}

// Load obtains a native typed operand without falling back to a previous value
// after a shell write. Invalid raw text is accepted when written and diagnosed
// only here, at the actual typed read's source position.
// Integer operands narrow to T's native representation. Operations requiring
// the source's wider scalar BEFORE result narrowing (division, comparisons,
// some conversions) must use ScalarValue instead of narrowing the operand.
func Load[T any](b *LexicalBindings, id string, site ValueSite) (T, error) {
	var zero T
	slot := b.slot(id)
	if slot == nil {
		return zero, lexicalUndefined(id, site)
	}
	if slot.value.Type() != reflect.TypeFor[T]() {
		return zero, &LexicalWriteError{id, "load type differs from binding type"}
	}
	if !*slot.present {
		return zero, lexicalUndefined(slot.name, site)
	}
	if !slot.raw.present {
		return slot.value.Interface().(T), nil
	}
	value, err := lexicalNativeScalar(slot, slot.raw.scalar, site)
	if err != nil {
		return zero, err
	}
	return value.Interface().(T), nil
}

func lexicalUndefined(name string, site ValueSite) error {
	if site.Name != "" {
		name = site.Name
	}
	return &ValueError{Code: "BASHPP-EEXPR-UNDEFINED", Message: "undefined: " + name, Site: site}
}

// ScalarValue exposes the immutable, possibly wider scalar observation for
// compiler-selected scalar operations. It executes no source or operators.
// A runtime integer operation wraps its RESULT, so prematurely narrowing a
// raw int8 value 128 before division or comparison would change source behavior.
func (b *LexicalBindings) ScalarValue(id string, site ValueSite) (constant.Value, bool, error) {
	slot := b.slot(id)
	if slot == nil || !*slot.present {
		return nil, false, lexicalUndefined(id, site)
	}
	if slot.raw.present {
		return slot.raw.scalar, true, nil
	}
	if slot.raw.nativeScalar != nil {
		return slot.raw.nativeScalar, true, nil
	}
	value := slot.value
	switch value.Kind() {
	case reflect.String:
		return constant.MakeString(value.String()), true, nil
	case reflect.Bool:
		return constant.MakeBool(value.Bool()), true, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return constant.MakeInt64(value.Int()), true, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return constant.MakeUint64(value.Uint()), true, nil
	case reflect.Float32, reflect.Float64:
		scalar := constant.MakeFloat64(value.Float())
		if scalar.Kind() != constant.Unknown {
			return scalar, true, nil
		}
	}
	return nil, true, &ValueError{Code: "BASHPP-EEXPR-OPERAND", Message: slot.name + " is not a scalar", Site: site}
}

// PrintValue preserves source print observation separately from typed Load:
// invalid numeric text can print as text, and int8's raw 128 prints 128 rather
// than its narrowed native representation -128.
func (b *LexicalBindings) PrintValue(id string, site ValueSite) (any, bool, error) {
	slot := b.slot(id)
	if slot == nil || !*slot.present {
		return nil, false, nil
	}
	if !slot.raw.present && slot.raw.nativeScalar == nil {
		return slot.value.Interface(), true, nil
	}
	value := slot.raw.scalar
	if !slot.raw.present {
		value = slot.raw.nativeScalar
	}
	switch value.Kind() {
	case constant.String:
		return constant.StringVal(value), true, nil
	case constant.Bool:
		return constant.BoolVal(value), true, nil
	case constant.Int:
		if n, ok := constant.Int64Val(value); ok {
			return n, true, nil
		}
		if n, ok := constant.Uint64Val(value); ok {
			return n, true, nil
		}
		return value.ExactString(), true, nil
	case constant.Float:
		f, _ := constant.Float64Val(value)
		return f, true, nil
	}
	return nil, true, &ValueError{Code: "BASHPP-EEXPR-OPERAND", Message: slot.name + " is not a scalar", Site: site}
}

// NativeWritten must follow every successfully committed native write,
// including same-value writes. Equality cannot detect that x=01 followed by
// typed x=1 must replace shell spelling 01 with the native projection 1.
func (b *LexicalBindings) NativeWritten(id string) error {
	slot := b.slot(id)
	if slot == nil {
		return &LexicalWriteError{id, "unknown binding ID"}
	}
	slot.raw.value, slot.raw.scalar, slot.raw.present = Var{}, nil, false
	slot.raw.nativeScalar = nil
	return nil
}

// NativeScalarWritten records exact compiler-retained scalar provenance after
// a native write, including float literals/operations whose shell rendering
// cannot be reconstructed from binary float64. It never guesses that provenance.
func (b *LexicalBindings) NativeScalarWritten(id string, scalar constant.Value) error {
	slot := b.slot(id)
	if slot == nil {
		return &LexicalWriteError{id, "unknown binding ID"}
	}
	if scalar == nil || scalar.Kind() == constant.Unknown {
		return &LexicalWriteError{id, "invalid exact scalar provenance"}
	}
	native, err := lexicalNativeScalar(slot, scalar, ValueSite{Name: slot.name})
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(native.Interface(), slot.value.Interface()) {
		return &LexicalWriteError{id, "exact scalar differs from committed native value"}
	}
	slot.raw.value, slot.raw.scalar, slot.raw.present = Var{}, nil, false
	slot.raw.nativeScalar = scalar
	return nil
}

func (slot *lexicalSlot) shellText() (string, error) {
	if slot.raw.present {
		return slot.raw.value.String(), nil
	}
	if scalar := slot.raw.nativeScalar; scalar != nil {
		switch scalar.Kind() {
		case constant.String:
			return constant.StringVal(scalar), nil
		default:
			return scalar.ExactString(), nil
		}
	}
	return ProjectErr(slot.value.Interface(), slot.kind)
}

// Address provenance is entry-owned and shared across captured name views.
// A pointer argument can target a binding declared AFTER the callee captured
// its names; freezing name visibility must not make such a native alias stale.
// Slots retain actual storage, preventing address reuse during this entry.
type lexicalAddress struct {
	typ     reflect.Type
	pointer uintptr
}
type lexicalAddresses struct {
	mu    sync.Mutex
	slots map[lexicalAddress]*lexicalSlot
}

type lexicalPending struct {
	name    string
	kind    Kind
	present bool
	raw     *lexicalRaw
}

// RebindSnapshot installs this fork's raw provenance on the ACTUAL cloned
// addresses, including hidden pointees reached only through pointer arguments.
// Call only after the compiler's shared Snapshot.Clone succeeds and before any
// child code runs. This reuses its address mapping and never copies a graph.
func (b *LexicalBindings) RebindSnapshot(snapshot *Snapshot) error {
	if snapshot == nil || !snapshot.started {
		return &SnapshotError{"lexical rebinding requires a completed snapshot"}
	}
	if err := snapshot.ctx.Err(); err != nil {
		return err
	}
	b.store.mu.Lock()
	defer b.store.mu.Unlock()
	b.addresses.mu.Lock()
	defer b.addresses.mu.Unlock()
	for source, destination := range snapshot.addresses {
		pending, ok := b.pending[lexicalAddress{source.typ, source.address}]
		if !ok {
			continue
		}
		key := lexicalAddress{destination.Type(), destination.Addr().Pointer()}
		for _, slot := range b.store.slots {
			if slot.value.Type() == key.typ && slot.value.Addr().Pointer() == key.pointer {
				slot.raw = pending.raw
			}
		}
		if existing := b.addresses.slots[key]; existing != nil {
			existing.raw = pending.raw
		} else {
			present := pending.present
			b.addresses.slots[key] = &lexicalSlot{value: destination, present: &present, name: pending.name, kind: pending.kind, raw: pending.raw}
		}
	}
	return nil
}

func (b *LexicalBindings) track(slot *lexicalSlot) {
	key := lexicalAddress{slot.value.Type(), slot.value.Addr().Pointer()}
	b.addresses.mu.Lock()
	defer b.addresses.mu.Unlock()
	if b.addresses.slots[key] == nil {
		b.addresses.slots[key] = slot
	}
}
func (b *LexicalBindings) slotAt(address any) *lexicalSlot {
	value := reflect.ValueOf(address)
	if value.Kind() != reflect.Pointer || value.IsNil() {
		return nil
	}
	key := lexicalAddress{value.Type().Elem(), value.Pointer()}
	b.addresses.mu.Lock()
	defer b.addresses.mu.Unlock()
	return b.addresses.slots[key]
}

// LoadAddress checks raw provenance when a pointer aliases a registered native
// binding. Unregistered pointers retain ordinary checked native dereferencing.
func LoadAddress[T any](b *LexicalBindings, address *T, site ValueSite) (T, error) {
	var zero T
	if address == nil {
		return zero, &ValueError{Code: "BASHPP-ENIL-DEREF", Message: "dereference of nil pointer", Site: site}
	}
	slot := b.slotAt(address)
	if slot == nil {
		return *address, nil
	}
	if !*slot.present {
		return zero, lexicalUndefined(slot.name, site)
	}
	if !slot.raw.present {
		return *address, nil
	}
	value, err := lexicalNativeScalar(slot, slot.raw.scalar, site)
	if err != nil {
		return zero, err
	}
	return value.Interface().(T), nil
}

// NativeWrittenAt invalidates raw spelling after a native write through an
// alias. An unregistered address needs no lexical metadata update.
func (b *LexicalBindings) NativeWrittenAt(address any) error {
	slot := b.slotAt(address)
	if slot == nil {
		return nil
	}
	slot.raw.value, slot.raw.scalar, slot.raw.present = Var{}, nil, false
	slot.raw.nativeScalar = nil
	return nil
}

func lexicalRawScalar(typ reflect.Type, text string) constant.Value {
	switch typ.Kind() {
	case reflect.String:
		return constant.MakeString(text)
	case reflect.Bool:
		return constant.MakeBool(text == "true")
	case reflect.Float32, reflect.Float64:
		if value := constant.MakeFromLiteral(text, token.FLOAT, 0); value.Kind() != constant.Unknown {
			return value
		}
	default:
		if value := constant.MakeFromLiteral(text, token.INT, 0); value.Kind() != constant.Unknown {
			return value
		}
	}
	// This fallback is observable: 09 is invalid as a Go integer literal but
	// valid as a floating scalar; abc remains a string until a typed read.
	if text == "true" || text == "false" {
		return constant.MakeBool(text == "true")
	}
	if value := constant.MakeFromLiteral(text, token.INT, 0); value.Kind() != constant.Unknown {
		return value
	}
	if value := constant.MakeFromLiteral(text, token.FLOAT, 0); value.Kind() != constant.Unknown {
		return value
	}
	return constant.MakeString(text)
}

func lexicalNativeScalar(slot *lexicalSlot, scalar constant.Value, site ValueSite) (reflect.Value, error) {
	typ := slot.value.Type()
	name := slot.raw.info.SourceType
	if name == "" {
		name = typ.String()
	}
	failure := func() (reflect.Value, error) {
		return reflect.Value{}, &ValueError{Code: "BASHPP-EEXPR-CONVERT", Message: fmt.Sprintf("cannot convert %s to %s", scalar.Kind(), name), Site: site}
	}
	result := reflect.New(typ).Elem()
	switch typ.Kind() {
	case reflect.String:
		if scalar.Kind() != constant.String {
			return failure()
		}
		result.SetString(constant.StringVal(scalar))
	case reflect.Bool:
		if scalar.Kind() != constant.Bool {
			return failure()
		}
		result.SetBool(constant.BoolVal(scalar))
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		integer := constant.ToInt(scalar)
		if integer.Kind() != constant.Int {
			return failure()
		}
		n, ok := new(big.Int).SetString(integer.ExactString(), 10)
		if !ok {
			return failure()
		}
		modulus := new(big.Int).Lsh(big.NewInt(1), uint(typ.Bits()))
		n.Mod(n, modulus)
		switch typ.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			if n.Bit(typ.Bits()-1) != 0 {
				n.Sub(n, modulus)
			}
			result.SetInt(n.Int64())
		default:
			result.SetUint(n.Uint64())
		}
	case reflect.Float32, reflect.Float64:
		if scalar.Kind() != constant.Int && scalar.Kind() != constant.Float {
			return failure()
		}
		var f float64
		if typ.Bits() == 32 {
			n, _ := constant.Float32Val(scalar)
			f = float64(n)
		} else {
			f, _ = constant.Float64Val(scalar)
		}
		if math.IsInf(f, 0) {
			return reflect.Value{}, &ValueError{Code: "BASHPP-EEXPR-CONVERT", Message: fmt.Sprintf("constant %s overflows %s", scalar, name), Site: site}
		}
		result.SetFloat(f)
	default:
		return failure()
	}
	return result, nil
}
