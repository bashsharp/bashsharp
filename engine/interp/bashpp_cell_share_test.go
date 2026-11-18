// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"go/constant"
	"reflect"
	"sync"
	"testing"
	"unsafe"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// cellFieldSentinel is a distinguishable value for one bashPPCell field type.
// A type it does not know is a field whose sharing story nobody has thought
// about yet, so it fails rather than skipping the field.
func cellFieldSentinel(t *testing.T, name string, typ reflect.Type) reflect.Value {
	t.Helper()
	switch typ {
	case reflect.TypeFor[expand.Variable]():
		return reflect.ValueOf(expand.Variable{Set: true, Kind: expand.String, Str: "sentinel"})
	case reflect.TypeFor[constant.Value]():
		return reflect.ValueOf(constant.MakeInt64(7))
	case reflect.TypeFor[syntax.BashPPTypeExpr]():
		return reflect.ValueOf(syntax.BashPPTypeExpr(&syntax.BashPPNamedType{Name: &syntax.Lit{Value: "sentinel"}}))
	}
	switch typ.Kind() {
	case reflect.Bool:
		return reflect.ValueOf(true)
	case reflect.String:
		return reflect.ValueOf("sentinel").Convert(typ)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return reflect.ValueOf(int64(7)).Convert(typ)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return reflect.ValueOf(uint64(7)).Convert(typ)
	case reflect.Float32, reflect.Float64:
		return reflect.ValueOf(7.5).Convert(typ)
	case reflect.Complex64, reflect.Complex128:
		return reflect.ValueOf(complex(7.5, 1.5)).Convert(typ)
	case reflect.Pointer:
		return reflect.New(typ.Elem())
	}
	t.Fatalf("bashPPCell field %s has type %s with no sentinel: teach cellFieldSentinel about it, "+
		"and make sure (*bashPPCell).storeFields copies the field", name, typ)
	return reflect.Value{}
}

// setUnexported writes v into an unexported struct field. The cell's fields
// are package-private by design; the test reaches them the only way reflect
// allows so that it stays exhaustive without a hand-maintained name list.
func setUnexported(field reflect.Value, v reflect.Value) {
	reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem().Set(v)
}

// TestBashPPCellFieldsCopied is the structural half of the aliased-cell
// discipline on the writer side: it fails when a field is added to bashPPCell
// but not to (*bashPPCell).storeFields, which is the only whole-cell store a
// shared cell may take. A missed field would make `x = y` silently drop part
// of the value for every cell in the interpreter, shared or not.
//
// It equally pins the ONE field storeFields must NOT copy. guard answers "can
// two host goroutines reach this storage", which belongs to the binding and
// not to the value stored in it, and it is read unlocked by every reader
// deciding whether to lock at all — so it has to stay write-once.
func TestBashPPCellFieldsCopied(t *testing.T) {
	t.Parallel()
	source := &bashPPCell{}
	value := reflect.ValueOf(source).Elem()
	typ := value.Type()
	for i := range typ.NumField() {
		name := typ.Field(i).Name
		if name == "guard" {
			continue
		}
		setUnexported(value.Field(i), cellFieldSentinel(t, name, typ.Field(i).Type))
	}

	guard := new(sync.RWMutex)
	target := &bashPPCell{guard: guard}
	target.storeFields(source)

	stored := reflect.ValueOf(target).Elem()
	for i := range typ.NumField() {
		name := typ.Field(i).Name
		got := reflect.NewAt(typ.Field(i).Type, unsafe.Pointer(stored.Field(i).UnsafeAddr())).Elem().Interface()
		if name == "guard" {
			if got != any(guard) {
				t.Errorf("storeFields overwrote guard: got %v, want the target's own %v", got, guard)
			}
			continue
		}
		want := reflect.NewAt(typ.Field(i).Type, unsafe.Pointer(value.Field(i).UnsafeAddr())).Elem().Interface()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("storeFields did not copy field %s: got %v, want %v — add it to (*bashPPCell).storeFields", name, got, want)
		}
	}
}
