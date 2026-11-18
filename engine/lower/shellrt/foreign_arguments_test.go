package shellrt

import (
	"reflect"
	"strings"
	"testing"
)

type foreignScalarHostObject struct{ called *bool }

func (v foreignScalarHostObject) String() string { *v.called = true; return "2.5" }
func (v foreignScalarHostObject) MarshalJSON() ([]byte, error) {
	*v.called = true
	return []byte("2.5"), nil
}

func TestForeignScalarArgument(t *testing.T) {
	for _, c := range []struct {
		value any
		kind  string
		want  any
	}{
		{"2.5", "float64", float64(2.5)},
		{float64(2.5), "float64", float64(2.5)},
		{"0x10", "int", int64(16)},
		{7, "int", 7},
		{int64(7), "int", int64(7)},
		{"true", "bool", true},
		{true, "bool", true},
	} {
		got, err := ForeignScalarArgument(c.value, c.kind)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Fatalf("%#v/%s: %#v, %v; want %#v", c.value, c.kind, got, err, c.want)
		}
	}
	for _, kind := range []string{"int", "float64", "bool"} {
		if _, err := ForeignScalarArgument("invalid", kind); err == nil || !strings.Contains(err.Error(), "invalid syntax") {
			t.Fatalf("%s: %v", kind, err)
		}
		called := false
		if _, err := ForeignScalarArgument(foreignScalarHostObject{&called}, kind); err == nil || called {
			t.Fatalf("host method invoked or object accepted: %v %v", called, err)
		}
	}
}
