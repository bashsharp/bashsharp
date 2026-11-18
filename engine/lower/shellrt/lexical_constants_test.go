package shellrt

import (
	"reflect"
	"testing"
)

func TestRegisterConstantShadowsWithoutStorageTheProgramCanWrite(t *testing.T) {
	b := NewLexicalBindings()
	const x int = 1
	if err := RegisterConstant(b, "local:905:x", "x", "int", x, KindScalar); err != nil {
		t.Fatal(err)
	}
	info, ok := b.ConstantInfo("local:905:x")
	if !ok || !info.Constant || !info.Readonly || info.SourceType != "int" {
		t.Fatalf("ConstantInfo = %+v/%v", info, ok)
	}
	// The shadow must not be reachable as an address: a constant has none, so
	// no pointer can alias it and no native write can arrive through one.
	slot := b.slot("local:905:x")
	if slot == nil {
		t.Fatal("constant registered no slot")
	}
	if b.slotAt(slot.value.Addr().Interface()) != nil {
		t.Fatal("constant shadow is reachable through the address registry")
	}
	text, err := slot.shellText()
	if err != nil || text != "1" {
		t.Fatalf("shell text = %q/%v", text, err)
	}
}

func TestRegisterConstantIsIdempotentPerResolvedIdentity(t *testing.T) {
	b := NewLexicalBindings()
	if err := RegisterConstant(b, "local:905:x", "x", "int", 1, KindScalar); err != nil {
		t.Fatal(err)
	}
	// A constant in scope across several shell regions is declared once. The
	// engine refuses `x redeclared in this block`, so re-registration of the
	// same identity and value has to be a no-op rather than an error.
	if err := RegisterConstant(b, "local:905:x", "x", "int", 1, KindScalar); err != nil {
		t.Fatalf("re-registration: %v", err)
	}
	if err := RegisterConstant(b, "local:905:x", "x", "int", 2, KindScalar); err == nil {
		t.Fatal("a constant re-registered with a different value was accepted")
	}
	if err := RegisterConstant(b, "local:905:x", "x", "int8", int8(1), KindScalar); err == nil {
		t.Fatal("a constant re-registered with a different type was accepted")
	}
}

func TestRegisterConstantKeepsVariableAndConstantIdentitiesDistinct(t *testing.T) {
	b := NewLexicalBindings()
	value, present := 7, true
	if err := Register(b, "local:12:x", "x", &value, &present, KindScalar); err != nil {
		t.Fatal(err)
	}
	if err := RegisterConstant(b, "local:12:x", "x", "int", 1, KindScalar); err == nil {
		t.Fatal("a constant replaced a variable binding of the same identity")
	}
	// An inner constant shadowing an outer variable is a separate identity and
	// takes the name; the variable's storage is untouched.
	if err := RegisterConstant(b, "local:40:x", "x", "int", 1, KindScalar); err != nil {
		t.Fatal(err)
	}
	if value != 7 {
		t.Fatalf("shadowed variable storage changed to %d", value)
	}
	constants, err := b.Constants()
	if err != nil {
		t.Fatal(err)
	}
	want := []ConstBinding{{ID: "local:40:x", Name: "x", SourceType: "int", Text: "1"}}
	if !reflect.DeepEqual(constants, want) {
		t.Fatalf("Constants() = %+v, want %+v", constants, want)
	}
}

func TestConstantsReportsMetadataForTheDeclarationPolicy(t *testing.T) {
	b := NewLexicalBindings()
	type Amount int
	if err := RegisterConstant(b, "local:1:total", "total", "Amount", Amount(3), KindScalar); err != nil {
		t.Fatal(err)
	}
	if err := RegisterConstant(b, "local:2:name", "name", "string", "ok", KindScalar); err != nil {
		t.Fatal(err)
	}
	value, present := 9, true
	if err := Register(b, "local:3:y", "y", &value, &present, KindScalar); err != nil {
		t.Fatal(err)
	}
	constants, err := b.Constants()
	if err != nil {
		t.Fatal(err)
	}
	// Name order, constants only, and the source type spelling rather than
	// Go's rendering of the underlying type.
	want := []ConstBinding{
		{ID: "local:2:name", Name: "name", SourceType: "string", Text: "ok"},
		{ID: "local:1:total", Name: "total", SourceType: "Amount", Text: "3"},
	}
	if !reflect.DeepEqual(constants, want) {
		t.Fatalf("Constants() = %+v, want %+v", constants, want)
	}
	if _, ok := b.ConstantInfo("local:3:y"); ok {
		t.Fatal("a variable binding reported constant metadata")
	}
}

// A repeated registration reaches an ID the store already holds whenever a
// declaration is re-executed — a loop body, a re-entered callable. If the name
// was rebound in between (an inner variable shadowing the spelling), returning
// early leaves the constant in the store but pointing nowhere, so the boundary
// silently stops projecting it and stops knowing the identity is constant.
func TestRegisterConstantRepublishesNameOnRepeatedRegistration(t *testing.T) {
	b := NewLexicalBindings()
	if err := RegisterConstant(b, "local:905:x", "x", "int", 1, KindScalar); err != nil {
		t.Fatal(err)
	}
	shadow, present := 42, true
	if err := Register(b, "local:12:x", "x", &shadow, &present, KindScalar); err != nil {
		t.Fatal(err)
	}
	if _, constant := b.ConstantInfo(b.names["x"]); constant {
		t.Fatal("the shadowing variable did not take the name")
	}

	// Re-entering the constant's scope: the store already has this ID.
	if err := RegisterConstant(b, "local:905:x", "x", "int", 1, KindScalar); err != nil {
		t.Fatal(err)
	}
	slot, ok := b.visible()["x"]
	if !ok {
		t.Fatal("a repeated registration left the constant invisible")
	}
	if !slot.raw.info.Constant {
		t.Fatal("the name did not return to the constant identity")
	}
	text, err := slot.shellText()
	if err != nil || text != "1" {
		t.Fatalf("shell text = %q/%v", text, err)
	}
	constants, err := b.Constants()
	if err != nil {
		t.Fatal(err)
	}
	want := []ConstBinding{{ID: "local:905:x", Name: "x", SourceType: "int", Text: "1"}}
	if !reflect.DeepEqual(constants, want) {
		t.Fatalf("Constants() = %+v, want %+v", constants, want)
	}
	if shadow != 42 {
		t.Fatalf("the shadowed variable's storage changed to %d", shadow)
	}
	// Still idempotent rather than a redeclaration, however often it repeats.
	for range 3 {
		if err := RegisterConstant(b, "local:905:x", "x", "int", 1, KindScalar); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := b.visible()["x"]; !ok {
		t.Fatal("a later registration lost the constant")
	}
}

// A fresh captured view that never saw the constant registers it from scratch,
// which must not disturb the parent's binding.
func TestRegisterConstantInFreshViewLeavesParentIntact(t *testing.T) {
	b := NewLexicalBindings()
	if err := RegisterConstant(b, "local:905:x", "x", "int", 1, KindScalar); err != nil {
		t.Fatal(err)
	}
	view := b.CaptureNames(map[string]string{})
	if _, ok := view.visible()["x"]; ok {
		t.Fatal("an uncaptured constant was already visible")
	}
	if err := RegisterConstant(view, "local:905:x", "x", "int", 1, KindScalar); err != nil {
		t.Fatal(err)
	}
	for _, bindings := range []*LexicalBindings{b, view} {
		slot, ok := bindings.visible()["x"]
		if !ok || !slot.raw.info.Constant {
			t.Fatalf("constant missing: visible=%v", ok)
		}
		text, err := slot.shellText()
		if err != nil || text != "1" {
			t.Fatalf("shell text = %q/%v", text, err)
		}
	}
}

func TestConstantForkRetainsImmutableMetadata(t *testing.T) {
	parent := NewLexicalBindings()
	if err := RegisterConstant(parent, "constant:x", "x", "int", 7, KindScalar); err != nil {
		t.Fatal(err)
	}
	child := parent.Fork()
	info, ok := child.ConstantInfo("constant:x")
	if !ok || !info.Constant || !info.Readonly {
		t.Fatalf("lost declaration: %#v %v", info, ok)
	}
	values, err := child.Constants()
	if err != nil || len(values) != 1 || values[0].Text != "7" {
		t.Fatalf("lost constant: %#v %v", values, err)
	}
}
