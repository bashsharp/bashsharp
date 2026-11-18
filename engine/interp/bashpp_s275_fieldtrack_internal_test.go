package interp

import (
	"strings"
	"testing"
)

func TestS275TrackedFieldCodecUsesNamedType(t *testing.T) {
	code, err := bashPPLocalCodecsGo([]bashPPLocalType{{Name: "T", Decl: "struct { X int `go:\"track\"` }"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(code, "reflect.TypeFor[T]()") {
		t.Fatal("missing named codec")
	}
	if strings.Contains(code, "reflect.TypeFor[struct") {
		t.Fatal("tracked field emitted in unnamed struct codec")
	}
}
