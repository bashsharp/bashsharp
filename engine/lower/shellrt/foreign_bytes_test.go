package shellrt

import "testing"

func TestForeignBytesProjection(t *testing.T) {
	for _, data := range [][]byte{nil, {}, {0, 0xff, 0x80, 'x'}} {
		want := string(data)
		wasNil := data == nil
		p := lexicalProgram(t)
		cell := Cell[[]byte](p.Bindings, "bytes", "bytes", KindForeignBytes)
		cell.Value, cell.Present = data, true
		lexicalValue(t, p.Bindings, p.Session, "bytes", want, true)
		if (cell.Value == nil) != wasNil || string(cell.Value) != want {
			t.Fatal("projection changed typed cell")
		}
		if len(data) > 0 {
			cell.Value[0] ^= 1
			if data[0] != cell.Value[0] {
				t.Fatal("typed slice identity lost")
			}
			cell.Value[0] ^= 1
		}
		got, err := ProjectErr(data, KindForeignBytes)
		if err != nil || got != want || ForeignBytesProjection(data) != want {
			t.Fatalf("projection %x: %q %v", data, got, err)
		}
		// Projection is read-only and the original typed slice remains usable.
		if string(data) != want {
			t.Fatal("projection mutated bytes")
		}
	}
	for _, value := range []any{nil, "text", []int{65}, foreignScalarHostObject{}} {
		if _, err := ProjectErr(value, KindForeignBytes); err == nil {
			t.Fatalf("accepted non-bytes %T", value)
		}
	}
	if _, err := ProjectErr([]byte{65}, KindScalar); err == nil {
		t.Fatal("ordinary scalar projection widened")
	}
}
