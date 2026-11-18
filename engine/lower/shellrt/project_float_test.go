// Copyright (c) 2026, the bash++ authors
// See LICENSE for licensing information

package shellrt

import "testing"

// KindFloat is runtime provenance: a float that crossed a foreign boundary
// renders with FormatFloat 'g', exactly as the interpreter renders a foreign
// call's float result. KindScalar keeps refusing floats, so retained literal
// provenance remains the only native float path.
func TestProjectKindFloat(t *testing.T) {
	for _, c := range []struct {
		value any
		want  string
	}{
		{2.5, "2.5"},
		{float64(2), "2"},
		{float32(0.5), "0.5"},
		{"text", "text"}, // a non-float falls back to the scalar rules
	} {
		got, err := ProjectErr(c.value, KindFloat)
		if err != nil || got != c.want {
			t.Fatalf("ProjectErr(%v, KindFloat) = %q, %v; want %q", c.value, got, err, c.want)
		}
	}
	if _, err := ProjectErr(2.5, KindScalar); err == nil {
		t.Fatal("KindScalar accepted a float without retained provenance")
	}
	if _, err := ProjectErr(new(struct{}), KindFloat); err == nil {
		t.Fatal("KindFloat accepted a pointer")
	}
}
