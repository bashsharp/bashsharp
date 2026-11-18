// Copyright (c) 2026, the bash++ authors
// See LICENSE for licensing information

package expand_test

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/go-quicktest/qt"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestObjectsEnabled(t *testing.T) {
	t.Parallel()

	// The gate is the dialect, and only bash++ opens it. In particular the
	// zero value — which is what every existing caller has — is closed.
	var zero expand.Config
	qt.Assert(t, qt.IsFalse(zero.ObjectsEnabled()))

	for _, lang := range []syntax.LangVariant{
		syntax.LangBash, syntax.LangPOSIX, syntax.LangMirBSDKorn,
		syntax.LangBats, syntax.LangZsh,
	} {
		cfg := expand.Config{Lang: lang}
		qt.Check(t, qt.IsFalse(cfg.ObjectsEnabled()), qt.Commentf("lang %v", lang))
	}

	cfg := expand.Config{Lang: syntax.LangBashPP}
	qt.Assert(t, qt.IsTrue(cfg.ObjectsEnabled()))
}

func TestObjectString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		val  any
		want string
	}{
		{"nil", nil, "null"},
		{"string", "hi", `"hi"`},
		{"int", 3, "3"},
		{"bool", true, "true"},
		{"slice", []int{1, 2}, "[1,2]"},
		{"map sorted", map[string]any{"b": 2, "a": 1}, `{"a":1,"b":2}`},
		{"nested", map[string]any{"x": []any{1, "two"}}, `{"x":[1,"two"]}`},
		{"struct", struct {
			Name string `json:"name"`
			N    int    `json:"n"`
		}{"gopher", 7}, `{"name":"gopher","n":7}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			qt.Assert(t, qt.Equals(expand.ObjectString(tc.val), tc.want))
			// A variable holding it coerces to the same string, which is
			// what the shell sees wherever a string is required.
			vr := expand.NewObject(tc.val)
			qt.Assert(t, qt.Equals(vr.Kind, expand.Object))
			qt.Assert(t, qt.IsTrue(vr.IsSet()))
			qt.Assert(t, qt.Equals(vr.String(), tc.want))
		})
	}
}

// TestObjectRoundTrip is the round-trip gate at the expand layer: a Go value
// put into an Object variable comes back out as the same Go value, undamaged,
// while still coercing to a string for anything that wants one.
func TestObjectRoundTrip(t *testing.T) {
	t.Parallel()

	type point struct {
		X, Y int
	}
	orig := point{1, 2}

	vr := expand.NewObject(orig)
	got, ok := vr.Obj.(point)
	qt.Assert(t, qt.IsTrue(ok))
	qt.Assert(t, qt.Equals(got, orig))
	qt.Assert(t, qt.Equals(vr.String(), `{"X":1,"Y":2}`))

	// Through a WriteEnviron, as the interpreter stores it: the Go value is
	// carried, not flattened to its string form on the way in.
	var we mapEnviron = map[string]expand.Variable{}
	qt.Assert(t, qt.IsNil(we.Set("P", vr)))
	back := we.Get("P")
	qt.Assert(t, qt.Equals(back.Kind, expand.Object))
	roundTripped, ok := back.Obj.(point)
	qt.Assert(t, qt.IsTrue(ok))
	qt.Assert(t, qt.Equals(roundTripped, orig))
	qt.Assert(t, qt.Equals(back.String(), `{"X":1,"Y":2}`))
}

// mapEnviron is a minimal [expand.WriteEnviron] for the round-trip test.
type mapEnviron map[string]expand.Variable

func (m mapEnviron) Get(name string) expand.Variable { return m[name] }

func (m mapEnviron) Each(fn func(string, expand.Variable) bool) {
	for name, vr := range m {
		if !fn(name, vr) {
			return
		}
	}
}

func (m mapEnviron) Set(name string, vr expand.Variable) error {
	if !vr.IsSet() {
		delete(m, name)
		return nil
	}
	m[name] = vr
	return nil
}

func TestValidObject(t *testing.T) {
	t.Parallel()

	qt.Assert(t, qt.IsNil(expand.ValidObject(nil)))
	qt.Assert(t, qt.IsNil(expand.ValidObject(map[string]any{"a": 1})))

	// Values which cannot become JSON are rejected up front, so that the
	// string coercion later on can never fail at a point where the shell has
	// no way to report it.
	qt.Assert(t, qt.IsNotNil(expand.ValidObject(make(chan int))))
	qt.Assert(t, qt.IsNotNil(expand.ValidObject(func() {})))
}

type panicJSONMarshaler struct{}

func (panicJSONMarshaler) MarshalJSON() ([]byte, error) {
	panic("MarshalJSON must not run")
}

type panicStringer struct{}

func (panicStringer) String() string {
	panic("String must not run")
}

type panicError struct{}

func (panicError) Error() string {
	panic("Error must not run")
}

type togglingJSONMarshaler struct {
	called *bool
}

func (m togglingJSONMarshaler) MarshalJSON() ([]byte, error) {
	*m.called = true
	return json.Marshal("called")
}

type embeddedHostile struct {
	Value togglingJSONMarshaler `json:"value"`
}

type embedsUnexportedHostile struct {
	embeddedHostile
}

type valueIsZeroBomb struct {
	called *bool
}

func (b valueIsZeroBomb) IsZero() bool {
	*b.called = true
	panic("IsZero must not run")
}

type pointerIsZeroBomb struct {
	called *bool
}

func (b *pointerIsZeroBomb) IsZero() bool {
	*b.called = true
	panic("IsZero must not run")
}

func TestObjectHostileMethodsRejectedWithoutCalling(t *testing.T) {
	t.Parallel()

	called := false
	values := []any{
		panicJSONMarshaler{},
		panicStringer{},
		panicError{},
		togglingJSONMarshaler{called: &called},
		struct {
			Field any
		}{Field: panicStringer{}},
		embedsUnexportedHostile{embeddedHostile{Value: togglingJSONMarshaler{called: &called}}},
	}
	var marker string
	for _, val := range values {
		qt.Assert(t, qt.IsNotNil(expand.ValidObject(val)))
		got := expand.ObjectString(val)
		qt.Assert(t, qt.Not(qt.Equals(got, "")))
		if marker == "" {
			marker = got
		}
		qt.Assert(t, qt.Equals(got, marker))
	}
	qt.Assert(t, qt.IsFalse(called))
}

func TestObjectOmitZeroMethodsRejectedWithoutCalling(t *testing.T) {
	t.Parallel()

	called := false
	values := []any{
		struct {
			Value valueIsZeroBomb `json:"value,omitzero"`
		}{Value: valueIsZeroBomb{called: &called}},
		struct {
			Value pointerIsZeroBomb `json:"value,omitzero"`
		}{Value: pointerIsZeroBomb{called: &called}},
		struct {
			Value *pointerIsZeroBomb `json:"value,omitzero"`
		}{Value: &pointerIsZeroBomb{called: &called}},
	}
	for _, val := range values {
		qt.Assert(t, qt.IsNotNil(expand.ValidObject(val)))
		qt.Assert(t, qt.Equals(expand.ObjectString(val), expand.ObjectString(make(chan int))))
	}
	qt.Assert(t, qt.IsFalse(called))
}

func TestObjectJSONDashTagExactness(t *testing.T) {
	t.Parallel()

	called := false
	omitted := struct {
		Value togglingJSONMarshaler `json:"-"`
	}{Value: togglingJSONMarshaler{called: &called}}
	qt.Assert(t, qt.IsNil(expand.ValidObject(omitted)))
	qt.Assert(t, qt.Equals(expand.ObjectString(omitted), `{}`))
	qt.Assert(t, qt.IsFalse(called))

	reachable := struct {
		Value togglingJSONMarshaler `json:"-,"`
	}{Value: togglingJSONMarshaler{called: &called}}
	qt.Assert(t, qt.IsNotNil(expand.ValidObject(reachable)))
	qt.Assert(t, qt.Equals(expand.ObjectString(reachable), expand.ObjectString(make(chan int))))
	qt.Assert(t, qt.IsFalse(called))
}

func TestObjectInvalidUTF8SizeAccounting(t *testing.T) {
	t.Parallel()

	invalid := strings.Repeat("\xff", 11<<20)
	qt.Assert(t, qt.IsNotNil(expand.ValidObject(invalid)))
	qt.Assert(t, qt.Equals(expand.ObjectString(invalid), expand.ObjectString(make(chan int))))
}

func TestObjectCycleDepthAndSizeRejected(t *testing.T) {
	t.Parallel()

	type node struct {
		Next *node
	}
	cyclic := &node{}
	cyclic.Next = cyclic
	qt.Assert(t, qt.IsNotNil(expand.ValidObject(cyclic)))

	var deep any = 1
	for range 102 {
		deep = []any{deep}
	}
	qt.Assert(t, qt.IsNotNil(expand.ValidObject(deep)))

	tooLarge := []string{strings.Repeat("x", 65<<20)}
	qt.Assert(t, qt.IsNotNil(expand.ValidObject(tooLarge)))

	marker := expand.ObjectString(cyclic)
	qt.Assert(t, qt.Not(qt.Equals(marker, "")))
	qt.Assert(t, qt.Equals(expand.ObjectString(deep), marker))

	largeObject := expand.NewObject(tooLarge)
	qt.Assert(t, qt.Equals(largeObject.String(), marker))
}

func TestObjectMutationAfterSetFailsClosedAtCoercion(t *testing.T) {
	t.Parallel()

	type holder struct {
		Value any `json:"value"`
	}
	h := &holder{Value: []int{1, 2}}
	qt.Assert(t, qt.IsNil(expand.ValidObject(h)))
	vr := expand.NewObject(h)
	qt.Assert(t, qt.Equals(vr.String(), `{"value":[1,2]}`))

	h.Value = h
	got := vr.String()
	qt.Assert(t, qt.Not(qt.Equals(got, "")))
	qt.Assert(t, qt.Equals(got, expand.ObjectString(make(chan int))))
}

func TestNewObjectInvalidInputFailsClosed(t *testing.T) {
	t.Parallel()

	vr := expand.NewObject(panicStringer{})
	qt.Assert(t, qt.Equals(vr.Kind, expand.Object))
	qt.Assert(t, qt.IsTrue(vr.IsSet()))
	_, stillHostile := vr.Obj.(panicStringer)
	qt.Assert(t, qt.IsFalse(stillHostile))
	qt.Assert(t, qt.Equals(vr.String(), expand.ObjectString(func() {})))
}

func TestValidObjectPreflightInvariants(t *testing.T) {
	t.Parallel()

	type ordinary struct {
		Name string `json:"name"`
		N    int    `json:"n"`
	}
	valid := []any{
		nil,
		"hi",
		3,
		true,
		[]int{1, 2},
		[2]int{1, 2},
		map[string]int{"a": 1},
		map[int]string{2: "two"},
		json.Number("12"),
		ordinary{Name: "gopher", N: 7},
	}
	for _, val := range valid {
		qt.Assert(t, qt.IsNil(expand.ValidObject(val)), qt.Commentf("%T", val))
	}

	invalid := []any{
		map[float64]string{1: "one"},
		complex64(1),
		math.NaN(),
		fmt.Stringer(panicStringer{}),
	}
	for _, val := range invalid {
		qt.Assert(t, qt.IsNotNil(expand.ValidObject(val)), qt.Commentf("%T", val))
	}
}
