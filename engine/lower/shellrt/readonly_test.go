package shellrt

import (
	"net/url"
	"strings"
	"testing"
)

func TestReadonlyDeepAliasesAndIndependentObjects(t *testing.T) {
	var state ReadonlyState
	root := map[string][]int{"ports": {80, 443}}
	alias := root
	if err := state.Mark("cfg", &root); err != nil {
		t.Fatal(err)
	}
	if err := state.CheckMutation("alias", &alias, alias["ports"], `["ports"][0]`, "slice"); err == nil || err.Error() != `BASHPP-EREADONLY-MUTATION: cannot mutate readonly value "cfg" through alias "alias" and path ["ports"][0]` {
		t.Fatal(err)
	}
	if err := state.CheckAssign(&root); err == nil || !strings.Contains(err.Error(), `cannot assign to readonly value "cfg"`) {
		t.Fatal(err)
	}
	other := map[string][]int{"ports": {80, 443}}
	if err := state.CheckMutation("other", &other, other["ports"], `["ports"][0]`, "slice"); err != nil {
		t.Fatal(err)
	}
	if err := state.CheckAssign(&alias); err != nil {
		t.Fatal("independent alias rebinding", err)
	}
	view := alias["ports"][1:]
	if err := state.CheckMutation("view", &view, view, "[0]", "slice"); err == nil {
		t.Fatal("overlapping subslice escaped readonly identity")
	}
}
func TestReadonlyImportedPointerAndCycles(t *testing.T) {
	endpoint, err := url.Parse("https://example.test/original")
	if err != nil {
		t.Fatal(err)
	}
	var state ReadonlyState
	if err := state.Mark("endpoint", &endpoint); err != nil {
		t.Fatal(err)
	}
	alias := endpoint
	if err := state.CheckMutation("alias", &alias, alias, ".Host", "field"); err == nil {
		t.Fatal("imported pointer alias escaped")
	}
	type node struct {
		Next  *node
		Value int
	}
	cycle := &node{}
	cycle.Next = cycle
	if err := state.Mark("cycle", &cycle); err != nil {
		t.Fatal(err)
	}
	if err := state.CheckMutation("cycle", &cycle, cycle, ".Value", "field"); err == nil {
		t.Fatal("cycle mutation escaped")
	}
}
func TestReadonlyRuntimeFailureStatus(t *testing.T) {
	var state ReadonlyState
	value := 1
	if err := state.Mark("value", &value); err != nil {
		t.Fatal(err)
	}
	err, ok := state.CheckAssign(&value).(*ReadonlyError)
	if !ok || err.ExitStatus() != 2 {
		t.Fatalf("%T: %v", err, err)
	}
}

func TestReadonlyPointerToFieldAndCyclicSlice(t *testing.T) {
	var state ReadonlyState
	root := struct{ Number int }{Number: 3}
	alias := &root.Number
	if err := state.Mark("root", &root); err != nil {
		t.Fatal(err)
	}
	if err := state.CheckMutation("alias", &alias, alias, "*alias", "field"); err == nil {
		t.Fatal("pointer to field bypassed readonly")
	}
	cyclic := make([]any, 1)
	cyclic[0] = cyclic
	if err := state.Mark("cyclic", &cyclic); err != nil {
		t.Fatal(err)
	}
}
