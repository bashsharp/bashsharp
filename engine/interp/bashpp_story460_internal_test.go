//go:build full

// Sprint: #209; Story: #460; Story-ID: d8e7d58f362b
//
// White-box negatives for the Barrier D owner-151 collection repairs, guarding
// the boundaries of the shared-cause fixes so they do not over-accept:
//
//   - BASHPP-ECOLLECTION-LENGTH: the constant-length fold is Go-source only and
//     never turns a fractional float into a length.
//   - BASHPP-EBUILTIN-TYPE / BASHPP-ECOLLECTION-ELEMENT: the large-unsigned
//     carrier is accepted only for a representable integer literal, and only in
//     Go-source mode; Classic Bash# keeps rejecting the string carrier.
package interp

import (
	"testing"

	"github.com/go-quicktest/qt"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func namedType(name string) syntax.BashPPTypeExpr {
	return &syntax.BashPPNamedType{Name: &syntax.Lit{Value: name}}
}

func TestStory460ArrayLengthGating(t *testing.T) {
	// Go-source folds an integral float length...
	n, err := (&Runner{bashPPGoSource: true}).bashPPArrayLength("1e1")
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(n, 10))

	// ...but never a fractional one.
	_, err = (&Runner{bashPPGoSource: true}).bashPPArrayLength("1.5")
	qt.Assert(t, qt.IsNotNil(err))

	// Classic Bash# keeps its integer-literal-only length: no float fold.
	_, err = (&Runner{}).bashPPArrayLength("1e1")
	qt.Assert(t, qt.IsNotNil(err))
}

func TestStory460IntegerCarrierBounded(t *testing.T) {
	// A representable large unsigned literal is the carrier...
	qt.Assert(t, qt.IsTrue(bashPPCollectionIntegerText("uint64", "18446744073709551615")))
	// ...but a non-integer spelling never is (no permissive string parsing)...
	qt.Assert(t, qt.IsFalse(bashPPCollectionIntegerText("uint64", "nope")))
	// ...and neither is an out-of-range value for the destination type.
	qt.Assert(t, qt.IsFalse(bashPPCollectionIntegerText("uint8", "256")))
}

func TestStory460ElementCarrierGoSourceOnly(t *testing.T) {
	uint64Type := namedType("uint64")
	// Go-source accepts the large-unsigned string carrier as an element...
	err := (&Runner{bashPPGoSource: true}).bashPPCheckCollectionValue("18446744073709551615", uint64Type)
	qt.Assert(t, qt.IsNil(err))
	// ...Classic Bash# still rejects a string carrier for an integer element...
	err = (&Runner{}).bashPPCheckCollectionValue("18446744073709551615", uint64Type)
	qt.Assert(t, qt.IsNotNil(err))
	// ...and a non-numeric string is rejected even in Go-source mode.
	err = (&Runner{bashPPGoSource: true}).bashPPCheckCollectionValue("nope", uint64Type)
	qt.Assert(t, qt.IsNotNil(err))
}

func TestStory460CollectionCarrierRequiresMatchingShape(t *testing.T) {
	r := &Runner{bashPPGoSource: true}
	integer := namedType("int")
	mapType := &syntax.BashPPCollectionType{Kind: "map", Key: integer, Element: integer}
	meta := &bashPPCollectionMeta{kind: "map", typ: mapType}

	// Collection metadata is not a license to carry an arbitrary object. The
	// payload must have the exact storage shape authenticated by that metadata.
	_, ok := r.bashPPGoSourceCollectionCarrier([]any{1}, meta)
	qt.Assert(t, qt.IsFalse(ok))

	meta.kind = "slice"
	_, ok = r.bashPPGoSourceCollectionCarrier([]any{1}, meta)
	qt.Assert(t, qt.IsFalse(ok))

	// Classic Bash# continues through expand.NewObject's established policy.
	meta = &bashPPCollectionMeta{kind: "slice", typ: &syntax.BashPPCollectionType{Kind: "slice", Element: integer}}
	_, ok = (&Runner{}).bashPPGoSourceCollectionCarrier([]any{1}, meta)
	qt.Assert(t, qt.IsFalse(ok))
}
