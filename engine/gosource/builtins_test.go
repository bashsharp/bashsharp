package gosource

import (
	"go/types"
	"strings"
	"testing"
)

func TestGoSourceTestBuiltins(t *testing.T) {
	const builtins = `package p
func f() {
	assert(true)
	trace("x")
}
`
	const unrelated = `package p
func f() int { return 1 }
`
	shadows := []string{
		"package p\nfunc assert(b bool) {}\nfunc f() { assert(true) }\n",
		"package p\nvar assert = 1\nvar _ = assert\n",
	}

	// TestBuiltins is process-wide once enabled, so pin the default-off
	// behavior before enabling it below.
	_, err := Parse(strings.NewReader(builtins), "off.go", Options{})
	list, ok := err.(ErrorList)
	if !ok || len(list) == 0 {
		t.Fatalf("default checker environment: %T %v", err, err)
	}
	first, ok := list[0].(types.Error)
	if !ok {
		t.Fatalf("first diagnostic: %T %v", list[0], list[0])
	}
	pos := first.Fset.Position(first.Pos)
	if pos.Filename != "off.go" || pos.Line != 3 || pos.Column != 2 || !strings.Contains(first.Msg, "undefined: assert") {
		t.Fatalf("first diagnostic: %s: %s", pos, first.Msg)
	}
	if _, err := Parse(strings.NewReader(unrelated), "unrelated-off.go", Options{}); err != nil {
		t.Fatalf("unrelated program with option off: %v", err)
	}
	for _, source := range shadows {
		if _, err := Parse(strings.NewReader(source), "shadow-off.go", Options{}); err != nil {
			t.Errorf("user-declared assert with option off: %v", err)
		}
	}

	if _, err := Parse(strings.NewReader(builtins), "on.go", Options{TestBuiltins: true}); err != nil {
		t.Fatalf("test builtins enabled: %v", err)
	}
	if _, err := Parse(strings.NewReader(unrelated), "unrelated-on.go", Options{TestBuiltins: true}); err != nil {
		t.Fatalf("unrelated program with option on: %v", err)
	}

	for _, source := range shadows {
		if _, err := Parse(strings.NewReader(source), "shadow.go", Options{TestBuiltins: true}); err != nil {
			t.Errorf("user-declared assert with option on: %v", err)
		}
	}

	dependency := PackageSpec{Path: "example/dep", Sources: []Source{{Name: "dep.go", Data: []byte("package dep\nfunc F() { assert(true) }\n")}}}
	main := []Source{{Name: "main.go", Data: []byte("package main\nimport \"example/dep\"\nfunc main() { dep.F() }\n")}}
	if _, err := Load(main, Options{Packages: []PackageSpec{dependency}, TestBuiltins: true}); err != nil {
		t.Fatalf("test builtins in explicit package: %v", err)
	}
}
