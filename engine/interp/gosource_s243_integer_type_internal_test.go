//go:build full

package interp

import "testing"

func TestS243IntegerCarrierTypeBoundaries(t *testing.T) {
	r := &Runner{bashPPTypes: map[string]bashPPType{
		"Peano":      {underlying: "*Peano"},
		"IntPointer": {underlying: "*int"},
		"Wide":       {underlying: "uint64"},
		"Alias":      {underlying: "Wide"},
		"CycleA":     {underlying: "CycleB"},
		"CycleB":     {underlying: "CycleA"},
	}}
	for _, name := range []string{"Peano", "IntPointer", "CycleA", "string"} {
		if got, ok := r.bashPPUnderlyingIntegerName(name); ok {
			t.Fatalf("%s resolved as integer %s", name, got)
		}
	}
	for _, name := range []string{"uint64", "Wide", "Alias"} {
		if got, ok := r.bashPPUnderlyingIntegerName(name); !ok || got != "uint64" {
			t.Fatalf("%s resolved as %s, %v", name, got, ok)
		}
	}
}
