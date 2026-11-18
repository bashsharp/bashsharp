//go:build full

package interp

// Sprint: #281; Story: #809; Story-ID: fac7e14af4a8

import "testing"

func TestS281NativeSliceElementsVisibleLength(t *testing.T) {
	elements := []bashPPBridgeValue{
		{Kind: "int", Type: "int", Text: "1"},
		{Kind: "int", Type: "int", Text: "2"},
		{Kind: "int", Type: "int", Text: "99"},
	}
	for name, value := range map[string]bashPPBridgeValue{
		"explicit visible prefix": {Kind: "slice", Storage: 1, Length: 2, Capacity: 3, Elements: elements},
		"explicit empty view":     {Kind: "slice", Capacity: 3, Elements: elements},
		"legacy headerless":       {Kind: "slice", Elements: elements},
		"nil":                     {Kind: "nil"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := nativeSliceElements(value)
			if err != nil {
				t.Fatal(err)
			}
			want := len(elements)
			switch name {
			case "explicit visible prefix":
				want = 2
			case "explicit empty view", "nil":
				want = 0
			}
			if len(got) != want {
				t.Fatalf("visible elements = %d, want %d", len(got), want)
			}
		})
	}
}

func TestS281NativeSliceElementsMalformedHeader(t *testing.T) {
	elements := []bashPPBridgeValue{{Kind: "int", Type: "int", Text: "1"}}
	for name, value := range map[string]bashPPBridgeValue{
		"negative offset":    {Kind: "slice", Offset: -1, Length: 1, Capacity: 1, Elements: elements},
		"negative length":    {Kind: "slice", Length: -1, Capacity: 1, Elements: elements},
		"length over cap":    {Kind: "slice", Length: 2, Capacity: 1, Elements: elements},
		"capacity over data": {Kind: "slice", Length: 1, Capacity: 2, Elements: elements},
		"data over capacity": {Kind: "slice", Length: 1, Capacity: 1, Elements: append(elements, elements[0])},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := nativeSliceElements(value); err == nil {
				t.Fatal("malformed slice header accepted")
			}
		})
	}
}
