package shellrt

import "testing"

// TestWord pins the nil/interface/pointer rendering boundary. Only an untyped
// nil interface — an unset interface binding or a nil error — renders empty, to
// match how the interpreter interpolates a nil interface value. Every ordinary
// value, including a typed nil boxed in an interface, keeps its fmt.Sprint
// spelling. This is a direct regression test for the deliberately general
// nil-interface rule in Word (as opposed to a narrower error-only seam).
func TestWord(t *testing.T) {
	var nilError error
	var nilAny any
	var nilPtr *int
	var nilMap map[string]int
	var nilSlice []int
	for _, tc := range []struct {
		name string
		in   any
		want string
	}{
		{"untyped nil interface", nil, ""},
		{"nil any variable", nilAny, ""},
		{"nil error interface", nilError, ""},
		{"nil error via any", any(nilError), ""},
		{"go.error success mints nil", GoError("f", 0), ""},
		{"go.error failure value", GoError("f", 1), "f: exit status 1"},
		{"go.error value directly", GoErrorValue("f: exit status 2"), "f: exit status 2"},
		// A typed nil is a non-nil interface: it must keep fmt.Sprint's <nil>
		// spelling, so the nil-interface rule stays narrow.
		{"typed nil pointer", nilPtr, "<nil>"},
		{"typed nil map", nilMap, "map[]"},
		{"typed nil slice", nilSlice, "[]"},
		// Ordinary scalars are unchanged.
		{"zero int", 0, "0"},
		{"int", 42, "42"},
		{"empty string", "", ""},
		{"string", "hello", "hello"},
		{"bool", true, "true"},
		{"negative", -1, "-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Word(tc.in); got != tc.want {
				t.Fatalf("Word(%#v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestNormalStatus pins the 8-bit wrap a decorator-set status takes before it
// becomes `$?` or a @go.error message, so the lowered runtime and the
// interpreter's uint8 truncation agree on the boundary values.
func TestNormalStatus(t *testing.T) {
	for _, tc := range []struct {
		in, want int
	}{
		{0, 0},
		{1, 1},
		{255, 255},
		{256, 0},
		{257, 1},
		{511, 255},
		{-1, 255},
		{-256, 0},
	} {
		if got := NormalStatus(tc.in); got != tc.want {
			t.Errorf("NormalStatus(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestGoErrorNormalizes proves GoError's presence and message use the same
// normalized status: an out-of-range status that wraps to zero is a nil error
// (a success), matching `$?`.
func TestGoErrorNormalizes(t *testing.T) {
	for _, tc := range []struct {
		status  int
		wantErr bool
		wantMsg string
	}{
		{0, false, ""},
		{255, true, "f: exit status 255"},
		{256, false, ""}, // wraps to 0 -> success, like $?
		{257, true, "f: exit status 1"},
		{-1, true, "f: exit status 255"},
	} {
		err := GoError("f", tc.status)
		if (err != nil) != tc.wantErr {
			t.Fatalf("GoError(f, %d) presence = %v, want %v", tc.status, err != nil, tc.wantErr)
		}
		if err != nil && err.Error() != tc.wantMsg {
			t.Fatalf("GoError(f, %d) = %q, want %q", tc.status, err.Error(), tc.wantMsg)
		}
	}
}
